package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	stdlog "log"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type networkHTTPS struct {
	gateway     *Gateway
	identity    string
	destination netip.AddrPort
	transport   *http.Transport
	mutex       sync.Mutex
	closed      bool
	handlers    sync.WaitGroup
}

type networkSingleListener struct {
	connection net.Conn
	accepted   bool
	done       chan struct{}
	once       sync.Once
}

type networkHTTPSConn struct {
	networkIdleConn
	gateway *Gateway
}

func (connection *networkHTTPSConn) Close() error {
	connection.gateway.release(connection.Conn)

	return nil
}

func (listener *networkSingleListener) Accept() (net.Conn, error) {
	if !listener.accepted {
		listener.accepted = true

		return listener.connection, nil
	}

	<-listener.done

	return nil, net.ErrClosed
}

func (listener *networkSingleListener) Close() error {
	listener.once.Do(func() {
		close(listener.done)
	})

	return nil
}

func (listener *networkSingleListener) Addr() net.Addr {
	return listener.connection.LocalAddr()
}

func (proxy *networkHTTPS) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	proxy.mutex.Lock()

	if proxy.closed {
		proxy.mutex.Unlock()

		return
	}

	proxy.handlers.Add(1)
	proxy.mutex.Unlock()

	defer proxy.handlers.Done()

	event := networkEvent{
		protocol:    "HTTPS",
		action:      "ERROR",
		target:      proxy.identity,
		destination: proxy.destination,
		method:      networkLogMethod(request.Method),
		reason:      "response interrupted",
		started:     time.Now(),
	}

	defer func() {
		if request.Context().Err() != nil {
			event.action = "CANCEL"
			event.reason = "request canceled"
		}

		proxy.gateway.logger.record(event)
	}()

	select {
	case proxy.gateway.requests <- struct{}{}:
		defer func() {
			<-proxy.gateway.requests
		}()
	default:
		event.status = http.StatusServiceUnavailable
		event.reason = "request limit"
		http.Error(writer, "gateway request limit", http.StatusServiceUnavailable)

		return
	}

	validHost := networkHTTPSAuthority(request.Host, proxy.identity)
	validURL := request.URL.Host == "" || networkHTTPSAuthority(request.URL.Host, proxy.identity)
	validScheme := request.URL.Scheme == "" || request.URL.Scheme == "https"

	if !validHost || !validURL || !validScheme || request.Method == http.MethodConnect || request.Header.Get("Upgrade") != "" || request.URL.User != nil {
		event.action = "DENY"
		event.status = http.StatusBadRequest
		event.reason = "invalid destination or unsupported tunnel"

		http.Error(writer, "invalid HTTPS destination or unsupported tunnel", http.StatusBadRequest)

		return
	}

	controller := http.NewResponseController(writer)
	controller.EnableFullDuplex()

	outgoing := request.Clone(request.Context())
	outgoing.RequestURI = ""
	outgoing.URL.Scheme = "https"
	outgoing.URL.Host = net.JoinHostPort(proxy.identity, "443")
	outgoing.Host = outgoing.URL.Host
	outgoing.Close = false
	outgoing.Trailer = request.Trailer

	removeNetworkHopHeaders(outgoing.Header)

	err := proxy.gateway.credentials.inject(outgoing, proxy.identity, proxy.gateway)
	if err != nil {
		if errors.Is(err, errCredentialEndpoint) {
			event.action = "DENY"
			event.status = http.StatusForbidden
			event.reason = "credential endpoint policy"

			http.Error(writer, errCredentialEndpoint.Error(), http.StatusForbidden)

			return
		}

		event.status = http.StatusBadGateway
		event.reason = "credential injection failed"

		http.Error(writer, oauthPublicError(err), http.StatusBadGateway)

		return
	}

	response, err := proxy.transport.RoundTrip(outgoing)
	if err != nil {
		event.reason = networkHTTPSError(err)

		if request.Context().Err() == nil {
			event.status = http.StatusBadGateway

			http.Error(writer, networkHTTPSError(err), http.StatusBadGateway)
		}

		return
	}

	defer response.Body.Close()

	removeNetworkHopHeaders(response.Header)

	maps.Copy(writer.Header(), response.Header)

	for name := range response.Trailer {
		writer.Header().Add("Trailer", name)
	}

	event.status = response.StatusCode

	writer.WriteHeader(response.StatusCode)

	err = controller.Flush()
	if err != nil {
		return
	}

	// Flush each bounded read. A blocked guest write stops upstream reads, preserving backpressure.
	var data [16 * 1024]byte

	for {
		count, readErr := response.Body.Read(data[:])
		if count > 0 {
			controller.SetWriteDeadline(time.Now().Add(networkIdle))

			_, err = writer.Write(data[:count])
			if err != nil {
				return
			}

			err = controller.Flush()
			if err != nil {
				return
			}
		}

		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				// Do not present a truncated upstream body as a successful complete response.
				panic(http.ErrAbortHandler)
			}

			break
		}
	}

	for name, values := range response.Trailer {
		writer.Header()[http.TrailerPrefix+name] = values
	}

	event.action = "REPLY"
	event.reason = ""
}

func (gateway *Gateway) interceptHTTPS(guest, upstream net.Conn, destination netip.AddrPort) {
	identity := ""

	configuration := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			identity = strings.ToLower(hello.ServerName)
			if identity == "" {
				identity = destination.Addr().String()
			}

			if !networkTLSIdentity(identity) {
				return nil, errors.New("unsupported TLS identity")
			}

			return gateway.ca.issue(identity)
		},
	}

	idle := &networkIdleConn{Conn: guest, timeout: networkDialTimeout}
	idle.SetDeadline(time.Now().Add(networkDialTimeout))

	connection := tls.Server(idle, configuration)

	err := connection.HandshakeContext(gateway.ctx)
	if err != nil {
		gateway.logger.record(networkEvent{protocol: "TLS", action: "ERROR", destination: destination, reason: "guest handshake failed"})

		return
	}

	defer connection.Close()

	gateway.logger.record(networkEvent{protocol: "TLS", action: "ACCEPT", target: identity, destination: destination})

	connection.SetDeadline(time.Time{})

	idle.timeout = networkIdle

	var upstreamOnce sync.Once

	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, protocol, address string) (net.Conn, error) {
			if protocol != "tcp" || address != net.JoinHostPort(identity, "443") {
				return nil, errNetworkDenied
			}

			var host net.Conn

			// Reuse the policy-checked TCP connection established before the guest SYN-ACK.
			upstreamOnce.Do(func() {
				host = upstream
			})

			if host == nil {
				var dialErr error

				host, dialErr = gateway.dial(ctx, "tcp", destination)
				if dialErr != nil {
					return nil, dialErr
				}
			}

			return &networkHTTPSConn{Conn: host, timeout: networkIdle, gateway: gateway}, nil
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, ServerName: identity},
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           8,
		MaxIdleConnsPerHost:    8,
		MaxConnsPerHost:        8,
		IdleConnTimeout:        networkIdle,
		TLSHandshakeTimeout:    networkDialTimeout,
		ResponseHeaderTimeout:  networkIdle,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 32 * 1024,
		WriteBufferSize:        16 * 1024,
		ReadBufferSize:         16 * 1024,
		DisableCompression:     true,
		HTTP2: &http.HTTP2Config{
			MaxReadFrameSize:              16384,
			MaxReceiveBufferPerConnection: networkBuffer,
			MaxReceiveBufferPerStream:     16 * 1024,
			StrictMaxConcurrentRequests:   true,
			SendPingTimeout:               30 * time.Second,
			PingTimeout:                   networkDialTimeout,
			WriteByteTimeout:              networkIdle,
		},
	}

	defer transport.CloseIdleConnections()

	proxy := &networkHTTPS{gateway: gateway, identity: identity, destination: destination, transport: transport}

	ctx, cancel := context.WithCancel(gateway.ctx)
	defer cancel()

	server := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: networkDialTimeout,
		IdleTimeout:       networkIdle,
		MaxHeaderBytes:    32 * 1024,
		ErrorLog:          stdlog.New(io.Discard, "", 0),
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams:          8,
			MaxReadFrameSize:              16384,
			MaxReceiveBufferPerConnection: networkBuffer,
			MaxReceiveBufferPerStream:     16 * 1024,
			SendPingTimeout:               30 * time.Second,
			PingTimeout:                   networkDialTimeout,
			WriteByteTimeout:              networkIdle,
		},
	}

	listener := &networkSingleListener{connection: connection, done: make(chan struct{})}

	server.ConnState = func(connection net.Conn, state http.ConnState) {
		if state == http.StateClosed || state == http.StateHijacked {
			listener.Close()
		}
	}

	server.BaseContext = func(net.Listener) context.Context {
		return ctx
	}

	server.Serve(listener)

	cancel()
	server.Close()

	proxy.mutex.Lock()
	proxy.closed = true
	proxy.mutex.Unlock()

	proxy.handlers.Wait()
}

func networkHTTPSError(err error) string {
	_, verification := errors.AsType[*tls.CertificateVerificationError](err)
	if verification {
		return "upstream HTTPS certificate verification failed"
	}

	_, unknownAuthority := errors.AsType[x509.UnknownAuthorityError](err)
	if unknownAuthority {
		return "upstream HTTPS certificate verification failed"
	}

	networkError, hasNetworkError := errors.AsType[net.Error](err)
	if hasNetworkError && networkError.Timeout() {
		return "upstream HTTPS connection timed out"
	}

	if errors.Is(err, errNetworkDenied) {
		return "upstream HTTPS connection denied by network policy"
	}

	// Never include raw transport errors: they can contain upstream-controlled data.
	return "upstream HTTPS connection or protocol failed"
}

func networkHTTPSAuthority(authority, identity string) bool {
	if strings.EqualFold(authority, identity) || authority == "["+identity+"]" {
		return true
	}

	host, port, err := net.SplitHostPort(authority)
	return err == nil && port == "443" && strings.EqualFold(host, identity)
}

func networkTLSIdentity(identity string) bool {
	address, err := netip.ParseAddr(identity)
	if err == nil {
		return address.Zone() == ""
	}

	if len(identity) == 0 || len(identity) > 253 {
		return false
	}

	for label := range strings.SplitSeq(identity, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}

	return true
}

func removeNetworkHopHeaders(headers http.Header) {
	for _, value := range headers.Values("Connection") {
		for name := range strings.SplitSeq(value, ",") {
			headers.Del(strings.TrimSpace(name))
		}
	}

	headers.Del("Connection")
	headers.Del("Proxy-Connection")
	headers.Del("Keep-Alive")
	headers.Del("Proxy-Authenticate")
	headers.Del("Proxy-Authorization")
	headers.Del("Te")
	headers.Del("Trailer")
	headers.Del("Transfer-Encoding")
	headers.Del("Upgrade")
}

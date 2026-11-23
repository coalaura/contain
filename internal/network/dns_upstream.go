package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const dnsUpstreamTimeout = 4 * time.Second

type dnsUpstreamConn struct {
	net.Conn
	gateway *Gateway
}

func (connection *dnsUpstreamConn) Close() error {
	connection.gateway.release(connection.Conn)

	return nil
}

func (gateway *Gateway) exchangeDNS(ctx context.Context, upstream string, wire []byte, query *dnsmessage.Message) ([]byte, *dnsmessage.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, dnsUpstreamTimeout)
	defer cancel()

	// Do not let an untrusted guest choose predictable upstream transaction IDs.
	var identifier [2]byte

	_, err := rand.Read(identifier[:])
	if err != nil {
		return nil, nil, err
	}

	request := append([]byte(nil), wire...)
	copy(request, identifier[:])

	expected := *query
	expected.ID = binary.BigEndian.Uint16(identifier[:])

	var response []byte

	if strings.HasPrefix(upstream, "https://") {
		response, err = gateway.exchangeDoH(ctx, upstream, request)
	} else {
		response, err = gateway.exchangePlainDNS(ctx, upstream, request, false)
	}

	if err != nil {
		return nil, nil, err
	}

	message, err := parseDNSResponse(response, &expected)
	if err != nil {
		return nil, nil, err
	}

	if message.Truncated && !strings.HasPrefix(upstream, "https://") {
		response, err = gateway.exchangePlainDNS(ctx, upstream, request, true)
		if err != nil {
			return nil, nil, err
		}

		message, err = parseDNSResponse(response, &expected)
		if err != nil {
			return nil, nil, err
		}
	}

	if message.Truncated || message.RCode == dnsmessage.RCodeServerFailure || message.RCode == dnsmessage.RCodeRefused {
		return nil, nil, errors.New("DNS upstream could not answer")
	}

	message.ID = query.ID
	binary.BigEndian.PutUint16(response, query.ID)

	return response, message, nil
}

func (gateway *Gateway) exchangePlainDNS(ctx context.Context, upstream string, wire []byte, stream bool) ([]byte, error) {
	protocol := "udp"

	if stream {
		protocol = "tcp"
	}

	connection, err := gateway.dialDNSUpstream(ctx, protocol, upstream)
	if err != nil {
		return nil, err
	}

	defer connection.Close()

	stop := context.AfterFunc(ctx, func() {
		connection.Close()
	})

	defer stop()

	deadline, _ := ctx.Deadline()
	connection.SetDeadline(deadline)

	var size [2]byte

	if stream {
		binary.BigEndian.PutUint16(size[:], uint16(len(wire)))

		_, err = connection.Write(size[:])
		if err != nil {
			return nil, err
		}
	}

	_, err = connection.Write(wire)
	if err != nil {
		return nil, err
	}

	if stream {
		_, err = io.ReadFull(connection, size[:])
		if err != nil {
			return nil, err
		}

		response := make([]byte, int(binary.BigEndian.Uint16(size[:])))
		_, err = io.ReadFull(connection, response)

		return response, err
	}

	return gateway.readDNSDatagram(connection)
}

func (gateway *Gateway) readDNSDatagram(reader io.Reader) ([]byte, error) {
	var buffer []byte

	select {
	case buffer = <-gateway.dnsBuffers:
	default:
		buffer = make([]byte, dnsResponseLimit)
	}

	length, err := reader.Read(buffer)
	if length > len(buffer)/2 {
		// Transfer large replies directly instead of allocating another large copy.
		return buffer[:length], err
	}

	response := bytes.Clone(buffer[:length])

	// Retain at most one scratch buffer per concurrent DNS query, rather than
	// allocating 64 KiB for every small UDP reply.
	select {
	case gateway.dnsBuffers <- buffer:
	default:
	}

	return response, err
}

func (gateway *Gateway) newDNSTransport() *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            gateway.dialDNSUpstream,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        8,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     true,
		TLSHandshakeTimeout:    dnsUpstreamTimeout,
		ResponseHeaderTimeout:  dnsUpstreamTimeout,
		MaxResponseHeaderBytes: 16 * 1024,
		HTTP2: &http.HTTP2Config{
			MaxReceiveBufferPerConnection: networkBuffer,
			MaxReceiveBufferPerStream:     16 * 1024,
			StrictMaxConcurrentRequests:   true,
			SendPingTimeout:               30 * time.Second,
			PingTimeout:                   dnsUpstreamTimeout,
		},
	}
}

func (gateway *Gateway) exchangeDoH(ctx context.Context, upstream string, wire []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")

	// RoundTrip avoids redirects and environment proxies for the configured resolver.
	response, err := gateway.dnsTransport.RoundTrip(request)
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()

	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || response.StatusCode != http.StatusOK || mediaType != "application/dns-message" {
		return nil, errors.New("invalid DNS-over-HTTPS response")
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, dnsResponseLimit+1))
	if err != nil || len(data) > dnsResponseLimit {
		return nil, errors.New("invalid or oversized DNS-over-HTTPS response")
	}

	ageValue := response.Header.Get("Age")
	if ageValue != "" {
		age, ageErr := strconv.ParseUint(ageValue, 10, 32)
		if ageErr != nil {
			return nil, errors.New("invalid DNS-over-HTTPS cache age")
		}

		var message dnsmessage.Message

		err = message.Unpack(data)
		if err != nil || len(message.Questions) != 1 {
			return nil, errDNSMessage
		}

		// HTTP caches age DoH responses independently of the TTLs in the wire body.
		for _, offset := range dnsTTLOffsets(data, &message) {
			ttl := binary.BigEndian.Uint32(data[offset:])
			binary.BigEndian.PutUint32(data[offset:], ttl-min(ttl, uint32(age)))
		}
	}

	return data, nil
}

func (gateway *Gateway) dialDNSUpstream(ctx context.Context, protocol, address string) (net.Conn, error) {
	if gateway.policy.mode == "closed" || gateway.ctx.Err() != nil {
		return nil, errNetworkDenied
	}

	select {
	case gateway.sockets <- struct{}{}:
	default:
		return nil, errors.New("gateway socket limit")
	}

	// Only administrator-configured upstreams reach this path. Their host-side
	// connections and hostname bootstrap are exempt from guest destination rules.
	dialer := net.Dialer{Timeout: dnsUpstreamTimeout}

	connection, err := dialer.DialContext(ctx, protocol, address)
	if err != nil {
		<-gateway.sockets

		return nil, err
	}

	counted := &networkSocketConn{Conn: connection, slots: gateway.sockets}
	if !gateway.track(counted) {
		return nil, context.Canceled
	}

	return &dnsUpstreamConn{Conn: counted, gateway: gateway}, nil
}

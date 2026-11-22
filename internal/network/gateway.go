package network

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	networkConnections      = 128
	networkBuffer           = 64 * 1024
	networkIdle             = 2 * time.Minute
	networkDialTimeout      = 10 * time.Second
	networkHeartbeatTimeout = 20 * time.Second
)

type Gateway struct {
	ctx         context.Context
	cancel      context.CancelFunc
	policy      *networkPolicy
	ca          *Authority
	credentials *Credentials
	base        string
	directory   string
	listener    net.Listener
	stack       *stack.Stack
	link        *channel.Endpoint
	ready       chan struct{}
	done        chan struct{}
	flows       chan struct{}
	sockets     chan struct{}
	requests    chan struct{}
	workers     sync.WaitGroup
	mutex       sync.Mutex
	writeMutex  sync.Mutex
	closed      bool
	connections map[net.Conn]struct{}
	transport   net.Conn
}

// Deadline refreshes bound idle connections without imposing a total streaming lifetime.
type networkIdleConn struct {
	net.Conn
	timeout       time.Duration
	deadlineMutex sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

type networkSocketConn struct {
	net.Conn
	slots chan struct{}
	once  sync.Once
}

type networkCloseWriter interface {
	CloseWrite() error
}

func (connection *networkSocketConn) Close() error {
	err := connection.Conn.Close()

	connection.once.Do(func() {
		<-connection.slots
	})

	return err
}

func (connection *networkSocketConn) CloseWrite() error {
	if writer, ok := connection.Conn.(networkCloseWriter); ok {
		return writer.CloseWrite()
	}

	return connection.Close()
}

func (connection *networkIdleConn) SetDeadline(deadline time.Time) error {
	connection.deadlineMutex.Lock()
	defer connection.deadlineMutex.Unlock()

	connection.readDeadline = deadline
	connection.writeDeadline = deadline

	return connection.Conn.SetDeadline(deadline)
}

func (connection *networkIdleConn) SetReadDeadline(deadline time.Time) error {
	connection.deadlineMutex.Lock()
	defer connection.deadlineMutex.Unlock()

	connection.readDeadline = deadline

	return connection.Conn.SetReadDeadline(deadline)
}

func (connection *networkIdleConn) SetWriteDeadline(deadline time.Time) error {
	connection.deadlineMutex.Lock()
	defer connection.deadlineMutex.Unlock()

	connection.writeDeadline = deadline

	return connection.Conn.SetWriteDeadline(deadline)
}

func (connection *networkIdleConn) Read(data []byte) (int, error) {
	connection.deadlineMutex.Lock()
	deadline := networkDeadline(connection.readDeadline, connection.timeout)
	connection.Conn.SetReadDeadline(deadline)
	connection.deadlineMutex.Unlock()

	return connection.Conn.Read(data)
}

func (connection *networkIdleConn) Write(data []byte) (int, error) {
	connection.deadlineMutex.Lock()
	deadline := networkDeadline(connection.writeDeadline, connection.timeout)
	connection.Conn.SetWriteDeadline(deadline)
	connection.deadlineMutex.Unlock()

	return connection.Conn.Write(data)
}

func (gateway *Gateway) Path() string {
	return gateway.base
}

func (gateway *Gateway) Done() <-chan struct{} {
	return gateway.done
}

func (gateway *Gateway) Close() error {
	gateway.mutex.Lock()

	if gateway.closed {
		gateway.mutex.Unlock()

		return nil
	}

	gateway.closed = true
	gateway.cancel()
	gateway.listener.Close()

	for connection := range gateway.connections {
		connection.Close()
	}

	gateway.mutex.Unlock()

	gateway.stack.Close()
	gateway.stack.Wait()
	gateway.workers.Wait()
	gateway.link.Close()

	return os.RemoveAll(gateway.directory)
}

func (gateway *Gateway) track(connection net.Conn) bool {
	gateway.mutex.Lock()
	defer gateway.mutex.Unlock()

	if gateway.closed || gateway.ctx.Err() != nil {
		connection.Close()

		return false
	}

	gateway.connections[connection] = struct{}{}

	return true
}

func (gateway *Gateway) release(connection net.Conn) {
	connection.Close()

	gateway.mutex.Lock()
	delete(gateway.connections, connection)
	gateway.mutex.Unlock()
}

func (gateway *Gateway) launch(work func()) bool {
	gateway.mutex.Lock()
	defer gateway.mutex.Unlock()

	if gateway.closed || gateway.ctx.Err() != nil {
		return false
	}

	select {
	case gateway.flows <- struct{}{}:
	default:
		return false
	}

	gateway.workers.Go(func() {
		defer func() {
			<-gateway.flows
		}()

		work()
	})

	return true
}

func (gateway *Gateway) dial(ctx context.Context, protocol string, destination netip.AddrPort) (net.Conn, error) {
	if gateway.ctx.Err() != nil {
		return nil, context.Canceled
	}

	if !networkDestination(destination.Addr()) || !gateway.policy.permits(destination.Addr()) || destination.Port() == 0 || protocol == "udp" && destination.Port() == 443 {
		return nil, errNetworkDenied
	}

	if protocol != "tcp" && protocol != "udp" {
		return nil, errNetworkPacket
	}

	select {
	case gateway.sockets <- struct{}{}:
	default:
		return nil, errors.New("gateway socket limit")
	}

	// Only numeric, validated addresses reach DialContext: no proxy or DNS redirection.
	dialer := net.Dialer{Timeout: networkDialTimeout, KeepAlive: 30 * time.Second}

	connection, err := dialer.DialContext(ctx, protocol, destination.String())
	if err != nil {
		<-gateway.sockets

		return nil, err
	}

	counted := &networkSocketConn{Conn: connection, slots: gateway.sockets}
	if !gateway.track(counted) {
		return nil, context.Canceled
	}

	return counted, nil
}

func (gateway *Gateway) forwardTCP(request *tcp.ForwarderRequest) {
	destination := networkEndpointDestination(request.ID())
	if !gateway.policy.permits(destination.Addr()) {
		request.Complete(true)

		return
	}

	started := gateway.launch(func() {
		// Do not acknowledge guest TCP until the original destination is reachable.
		// Otherwise Happy Eyeballs selects an unusable address family, especially for TLS.
		host, err := gateway.dial(gateway.ctx, "tcp", destination)
		if err != nil {
			request.Complete(true)

			return
		}

		var queue waiter.Queue

		endpoint, endpointErr := request.CreateEndpoint(&queue)
		request.Complete(endpointErr != nil)

		if endpointErr != nil {
			gateway.release(host)

			return
		}

		defer gateway.retireTCP(endpoint)
		defer gateway.release(host)

		guest := gonet.NewTCPConn(&queue, endpoint)
		if !gateway.track(guest) {
			return
		}

		defer gateway.release(guest)

		if destination.Port() == 443 {
			gateway.interceptHTTPS(guest, host, destination)

			return
		}

		relayNetwork(guest, host, networkIdle)
	})

	if !started {
		request.Complete(true)
	}
}

func (gateway *Gateway) retireTCP(endpoint tcpip.Endpoint) {
	// Keep closing and TIME_WAIT endpoints inside the flow budget, including queued data.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	timer := time.NewTimer(networkIdle)
	defer timer.Stop()

	for {
		state := tcp.EndpointState(endpoint.State())
		if state == tcp.StateClose || state == tcp.StateError {
			return
		}

		select {
		case <-ticker.C:
		case <-timer.C:
			endpoint.Abort()

			return
		case <-gateway.ctx.Done():
			endpoint.Abort()

			return
		}
	}
}

func (gateway *Gateway) forwardUDP(request *udp.ForwarderRequest) bool {
	destination := networkEndpointDestination(request.ID())
	if !gateway.policy.permits(destination.Addr()) || destination.Port() == 443 {
		return false
	}

	// Create synchronously to register the tuple before another datagram arrives.
	var queue waiter.Queue

	endpoint, endpointErr := request.CreateEndpoint(&queue)
	if endpointErr != nil {
		return true
	}

	guest := gonet.NewUDPConn(&queue, endpoint)

	started := gateway.launch(func() {
		if !gateway.track(guest) {
			return
		}

		defer gateway.release(guest)

		host, err := gateway.dial(gateway.ctx, "udp", destination)
		if err != nil {
			return
		}

		defer gateway.release(host)
		relayNetwork(guest, host, 30*time.Second)
	})

	if !started {
		guest.Close()
	}

	return true
}

func (gateway *Gateway) serve() {
	defer gateway.workers.Done()
	defer close(gateway.done)
	defer gateway.disconnect()

	connection, err := gateway.listener.Accept()
	gateway.listener.Close()

	if err != nil || !gateway.track(connection) {
		return
	}

	defer gateway.release(connection)

	gateway.transport = connection

	var handshake [8]byte

	connection.SetDeadline(time.Now().Add(networkDialTimeout))

	_, err = io.ReadFull(connection, handshake[:])
	if err != nil || string(handshake[:]) != "CONTAIN1" {
		return
	}

	_, err = connection.Write(handshake[:])
	if err != nil {
		return
	}

	connection.SetDeadline(time.Time{})

	close(gateway.ready)

	gateway.workers.Add(1)
	go gateway.writePackets()

	var (
		frame [networkMTU]byte
		size  [2]byte
	)

	for {
		connection.SetReadDeadline(time.Now().Add(networkHeartbeatTimeout))

		_, err = io.ReadFull(connection, size[:])
		if err != nil {
			return
		}

		length := int(binary.BigEndian.Uint16(size[:]))
		if length > len(frame) {
			return
		}

		if length == 0 {
			err = gateway.writeFrame(nil)
			if err != nil {
				return
			}

			continue
		}

		_, err = io.ReadFull(connection, frame[:length])
		if err != nil {
			return
		}

		parsed, packetErr := inspectNetworkPacket(frame[:length], gateway.policy)
		if packetErr != nil {
			continue
		}

		if parsed.protocol == 1 || parsed.protocol == 58 {
			gateway.forwardEcho(frame[:length], parsed)

			continue
		}

		protocol := ipv4.ProtocolNumber

		if parsed.source.Is6() {
			protocol = ipv6.ProtocolNumber
		}

		// Netstack retains packet buffers beyond InjectInbound; copy the reused frame.
		packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), frame[:length]...))})
		gateway.link.InjectInbound(protocol, packet)
		packet.DecRef()
	}
}

func (gateway *Gateway) disconnect() {
	gateway.mutex.Lock()
	defer gateway.mutex.Unlock()

	gateway.cancel()

	for connection := range gateway.connections {
		connection.Close()
	}
}

func (gateway *Gateway) writePackets() {
	defer gateway.workers.Done()

	for {
		packet := gateway.link.ReadContext(gateway.ctx)
		if packet == nil {
			return
		}

		view := packet.ToView()
		packet.DecRef()
		err := gateway.writeFrame(view.AsSlice())
		view.Release()

		if err != nil {
			gateway.transport.Close()

			return
		}
	}
}

func (gateway *Gateway) writeFrame(packet []byte) error {
	if len(packet) > networkMTU {
		return errNetworkPacket
	}

	gateway.writeMutex.Lock()
	defer gateway.writeMutex.Unlock()

	var frame [networkMTU + 2]byte

	binary.BigEndian.PutUint16(frame[:2], uint16(len(packet)))
	copy(frame[2:], packet)

	gateway.transport.SetWriteDeadline(time.Now().Add(networkDialTimeout))

	_, err := gateway.transport.Write(frame[:len(packet)+2])
	return err
}

func (gateway *Gateway) WaitReady(ctx context.Context) error {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	select {
	case <-gateway.ready:
		return nil
	case <-gateway.done:
		return errors.New("network gateway transport failed before readiness")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("guest network helper did not connect")
	}
}

func Start(ctx context.Context, dataDirectory string, config Config, ca *Authority, credentials *Credentials) (*Gateway, error) {
	policy, err := compileNetworkPolicy(config)
	if err != nil {
		return nil, err
	}

	directory, err := os.MkdirTemp(dataDirectory, "net-")
	if err != nil {
		return nil, err
	}

	err = protectNetworkDirectory(directory)
	if err != nil {
		os.RemoveAll(directory)

		return nil, err
	}

	base := filepath.Join(directory, "vsock")
	address := base + "_" + strconv.Itoa(networkPort)

	listener, err := net.Listen("unix", address)
	if err != nil {
		os.RemoveAll(directory)

		return nil, fmt.Errorf("listen for vsock gateway (Unix socket path %q): %w", address, err)
	}

	ctx, cancel := context.WithCancel(ctx)

	gateway := &Gateway{
		ctx:         ctx,
		cancel:      cancel,
		policy:      policy,
		ca:          ca,
		credentials: credentials,
		base:        base,
		directory:   directory,
		listener:    listener,
		ready:       make(chan struct{}),
		done:        make(chan struct{}),
		flows:       make(chan struct{}, networkConnections),
		sockets:     make(chan struct{}, networkConnections),
		requests:    make(chan struct{}, 64),
		connections: make(map[net.Conn]struct{}, networkConnections*2),
		link:        channel.New(128, networkMTU, ""),
		stack: stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocolWithOptions(ipv4.Options{AllowExternalLoopbackTraffic: true}), ipv6.NewProtocolWithOptions(ipv6.Options{AllowExternalLoopbackTraffic: true})},
			TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		}),
	}

	stackErr := configureNetworkStack(gateway)
	if stackErr != nil {
		gateway.Close()

		return nil, fmt.Errorf("configure network stack: %s", stackErr)
	}

	gateway.workers.Add(1)
	go gateway.serve()

	return gateway, nil
}

func configureNetworkStack(gateway *Gateway) tcpip.Error {
	stackErr := gateway.stack.CreateNIC(1, gateway.link)
	if stackErr != nil {
		return stackErr
	}

	stackErr = gateway.stack.SetPromiscuousMode(1, true)
	if stackErr != nil {
		return stackErr
	}

	stackErr = gateway.stack.SetSpoofing(1, true)
	if stackErr != nil {
		return stackErr
	}

	gateway.stack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}, {Destination: header.IPv6EmptySubnet, NIC: 1}})

	send := tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: networkBuffer, Max: networkBuffer}
	receive := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: networkBuffer, Max: networkBuffer}

	stackErr = gateway.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &send)
	if stackErr != nil {
		return stackErr
	}

	stackErr = gateway.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &receive)
	if stackErr != nil {
		return stackErr
	}

	stackErr = gateway.stack.SetTransportProtocolOption(tcp.ProtocolNumber, new(tcpip.TCPSynRetriesOption(2)))
	if stackErr != nil {
		return stackErr
	}

	forwardTCP := tcp.NewForwarder(gateway.stack, networkBuffer, networkConnections, gateway.forwardTCP)
	forwardUDP := udp.NewForwarder(gateway.stack, gateway.forwardUDP)

	gateway.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, forwardTCP.HandlePacket)
	gateway.stack.SetTransportProtocolHandler(udp.ProtocolNumber, forwardUDP.HandlePacket)

	return nil
}

func networkEndpointDestination(endpoint stack.TransportEndpointID) netip.AddrPort {
	address, _ := netip.AddrFromSlice(endpoint.LocalAddress.AsSlice())
	return netip.AddrPortFrom(address.Unmap(), endpoint.LocalPort)
}

func relayNetwork(guest, host net.Conn, timeout time.Duration) {
	finished := make(chan struct{})

	guestIdle := &networkIdleConn{Conn: guest, timeout: timeout}
	hostIdle := &networkIdleConn{Conn: host, timeout: timeout}

	go func() {
		defer close(finished)

		var data [networkBuffer]byte

		_, err := io.CopyBuffer(hostIdle, guestIdle, data[:])
		finishNetworkWrite(host, guest, err)
	}()

	var data [networkBuffer]byte

	_, err := io.CopyBuffer(guestIdle, hostIdle, data[:])
	finishNetworkWrite(guest, host, err)

	<-finished
}

func finishNetworkWrite(destination, source net.Conn, err error) {
	if writer, ok := destination.(networkCloseWriter); ok && err == nil {
		writer.CloseWrite()

		return
	}

	destination.Close()
	source.Close()
}

func networkDeadline(deadline time.Time, timeout time.Duration) time.Time {
	idle := time.Now().Add(timeout)
	if deadline.IsZero() || idle.Before(deadline) {
		return idle
	}

	return deadline
}

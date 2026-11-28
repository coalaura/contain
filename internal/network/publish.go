package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

var publishAddress = netip.MustParseAddr("192.0.2.3")

func (gateway *Gateway) publishPorts(values []string) error {
	ports, err := ResolvePorts(values)
	if err != nil {
		return err
	}

	gateway.published = make([]io.Closer, 0, len(ports))

	for _, port := range ports {
		protocol := port.Protocol + "4"

		if port.Host.Addr().Is6() {
			protocol = port.Protocol + "6"
		}

		if port.Protocol == "udp" {
			listener, err := net.ListenUDP(protocol, net.UDPAddrFromAddrPort(port.Host))
			if err != nil {
				return fmt.Errorf("publish %s/udp: %w", port.Host, err)
			}

			gateway.published = append(gateway.published, listener)

			gateway.workers.Go(func() {
				gateway.servePublishedUDP(listener, port)
			})
		} else {
			listener, err := net.Listen(protocol, port.Host.String())
			if err != nil {
				return fmt.Errorf("publish %s/tcp: %w", port.Host, err)
			}

			gateway.published = append(gateway.published, listener)

			gateway.workers.Go(func() {
				gateway.servePublishedTCP(listener, port)
			})
		}

		gateway.logger.record(networkEvent{protocol: strings.ToUpper(port.Protocol), action: "PUBLISH", destination: port.Host, reason: fmt.Sprintf("guest port %d", port.Guest)})
	}

	return nil
}

func (gateway *Gateway) publishReady() bool {
	select {
	case <-gateway.ready:
		return gateway.ctx.Err() == nil
	case <-gateway.ctx.Done():
		return false
	}
}

func (gateway *Gateway) servePublishedTCP(listener net.Listener, port Port) {
	if !gateway.publishReady() {
		return
	}

	for {
		host, err := listener.Accept()
		if err != nil {
			return
		}

		started := gateway.launch(func() {
			gateway.connectPublishedTCP(host, port)
		})

		if !started {
			host.Close()
		}
	}
}

func (gateway *Gateway) connectPublishedTCP(host net.Conn, port Port) {
	if !gateway.track(host) {
		return
	}

	defer gateway.release(host)

	ctx, cancel := context.WithTimeout(gateway.ctx, networkDialTimeout)
	defer cancel()

	local, remote := publishedAddresses(port.Guest)

	guest, err := gonet.DialTCPWithBind(ctx, gateway.stack, local, remote, ipv4.ProtocolNumber)
	if err != nil {
		gateway.logger.record(networkEvent{protocol: "TCP", action: "PUBLISH_FAIL", destination: port.Host, reason: err.Error()})

		return
	}

	// Retain closing endpoints in the shared flow budget, like outbound TCP.
	identifier := stack.TransportEndpointID{
		LocalAddress:  local.Addr,
		LocalPort:     uint16(guest.LocalAddr().(*net.TCPAddr).Port),
		RemoteAddress: remote.Addr,
		RemotePort:    remote.Port,
	}

	endpoint := gateway.stack.FindTransportEndpoint(ipv4.ProtocolNumber, tcp.ProtocolNumber, identifier, 1)
	if endpoint, ok := endpoint.(tcpip.Endpoint); ok {
		defer gateway.retireTCP(endpoint)
	}

	if !gateway.track(guest) {
		return
	}

	defer gateway.release(guest)

	relayNetwork(guest, host, networkIdle)
}

func publishedAddresses(port uint16) (tcpip.FullAddress, tcpip.FullAddress) {
	local := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(publishAddress.As4())}
	remote := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(guestAddress4.As4()), Port: port}

	return local, remote
}

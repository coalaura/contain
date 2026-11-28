package network

import (
	"net"
	"net/netip"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

const publishUDPIdle = 30 * time.Second

type publishedUDP struct {
	gateway  *Gateway
	listener *net.UDPConn
	port     Port
	mutex    sync.Mutex
	peers    map[netip.AddrPort]*gonet.UDPConn
}

func (publisher *publishedUDP) peer(address netip.AddrPort) *gonet.UDPConn {
	publisher.mutex.Lock()
	defer publisher.mutex.Unlock()

	if guest, ok := publisher.peers[address]; ok {
		return guest
	}

	// Reserve a shared flow slot before creating a netstack endpoint.
	select {
	case publisher.gateway.flows <- struct{}{}:
	default:
		return nil
	}

	local, remote := publishedAddresses(publisher.port.Guest)

	guest, err := gonet.DialUDP(publisher.gateway.stack, &local, &remote, ipv4.ProtocolNumber)
	if err != nil {
		<-publisher.gateway.flows

		return nil
	}

	if !publisher.gateway.track(guest) {
		<-publisher.gateway.flows

		return nil
	}

	publisher.peers[address] = guest

	publisher.gateway.workers.Go(func() {
		defer func() {
			publisher.mutex.Lock()
			delete(publisher.peers, address)
			publisher.mutex.Unlock()

			publisher.gateway.release(guest)
			<-publisher.gateway.flows
		}()

		publisher.reply(address, guest)
	})

	return guest
}

func (publisher *publishedUDP) reply(address netip.AddrPort, guest *gonet.UDPConn) {
	var data [networkBuffer]byte

	for {
		guest.SetReadDeadline(time.Now().Add(publishUDPIdle))

		length, err := guest.Read(data[:])
		if err != nil {
			return
		}

		_, err = publisher.listener.WriteToUDPAddrPort(data[:length], address)
		if err != nil {
			return
		}
	}
}

func (gateway *Gateway) servePublishedUDP(listener *net.UDPConn, port Port) {
	if !gateway.publishReady() {
		return
	}

	publisher := publishedUDP{
		gateway:  gateway,
		listener: listener,
		port:     port,
		peers:    make(map[netip.AddrPort]*gonet.UDPConn, networkConnections),
	}

	var data [networkBuffer]byte

	for {
		length, address, err := listener.ReadFromUDPAddrPort(data[:])
		if err != nil {
			return
		}

		guest := publisher.peer(address)
		if guest == nil {
			continue
		}

		guest.SetReadDeadline(time.Now().Add(publishUDPIdle))
		guest.SetWriteDeadline(time.Now().Add(networkDialTimeout))

		_, err = guest.Write(data[:length])
		if err != nil {
			guest.Close()
		}
	}
}

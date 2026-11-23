package network

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

var dnsAddress = netip.MustParseAddr("192.0.2.1")

func (policy *networkPolicy) servesDNS(destination netip.AddrPort) bool {
	return policy.mode != "closed" && destination.Addr() == dnsAddress && destination.Port() == 53
}

func (gateway *Gateway) resolveDNS(ctx context.Context, wire []byte, query *dnsmessage.Message) (response []byte) {
	started := time.Now()
	source := "upstream"

	defer func() {
		gateway.logDNS(query, response, started, source)
	}()

	if !gateway.policy.permitsDomain(query.Questions[0].Name.String()) {
		source = "domain policy"

		return dnsReply(query, dnsmessage.RCodeRefused, false)
	}

	// Include flags, question case and EDNS options in the cache key; only the ID varies.
	key := string(wire[2:])

	cached := gateway.policy.dns.lookup(key, query.ID)
	if cached != nil {
		source = "cache"

		return cached
	}

	response, source = gateway.queryDNS(ctx, key, wire, query)

	return response
}

func (gateway *Gateway) serveDNSTCP(connection net.Conn) {
	var (
		buffer [dnsQueryLimit + 1]byte
		size   [2]byte
	)

	for {
		connection.SetDeadline(time.Now().Add(30 * time.Second))

		_, err := io.ReadFull(connection, size[:])
		if err != nil {
			return
		}

		length := int(binary.BigEndian.Uint16(size[:]))
		if length > dnsQueryLimit {
			return
		}

		_, err = io.ReadFull(connection, buffer[:length])
		if err != nil {
			return
		}

		query, parseErr := parseDNSQuery(buffer[:length])
		if parseErr != nil {
			return
		}

		response := gateway.resolveDNS(gateway.ctx, buffer[:length], query)

		binary.BigEndian.PutUint16(size[:], uint16(len(response)))

		_, err = connection.Write(size[:])
		if err != nil {
			return
		}

		_, err = connection.Write(response)
		if err != nil {
			return
		}
	}
}

func (gateway *Gateway) forwardDNSTCP(request *tcp.ForwarderRequest) {
	started := gateway.launch(func() {
		var queue waiter.Queue

		endpoint, endpointErr := request.CreateEndpoint(&queue)
		request.Complete(endpointErr != nil)

		if endpointErr != nil {
			return
		}

		defer gateway.retireTCP(endpoint)

		guest := gonet.NewTCPConn(&queue, endpoint)
		if !gateway.track(guest) {
			return
		}

		defer gateway.release(guest)
		gateway.serveDNSTCP(guest)
	})

	if !started {
		request.Complete(true)
	}
}

func (gateway *Gateway) lookupDNSAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if !gateway.policy.permitsDomain(host) {
		return nil, errNetworkDenied
	}

	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, err
	}

	addresses := make([]netip.Addr, 0, dnsAddressLimit)
	queryTypes := [2]dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}

	for _, queryType := range queryTypes {
		query := dnsmessage.Message{
			RecursionDesired: true,
			Questions:        []dnsmessage.Question{{Name: name, Type: queryType, Class: dnsmessage.ClassINET}},
		}

		wire, packErr := query.Pack()
		if packErr != nil {
			return nil, packErr
		}

		response := gateway.resolveDNS(ctx, wire, &query)

		message, parseErr := parseDNSResponse(response, &query)
		if parseErr != nil || message.RCode != dnsmessage.RCodeSuccess {
			continue
		}

		resolved, addressErr := gateway.policy.dnsAddresses(message)
		if addressErr != nil {
			return nil, addressErr
		}

		if len(addresses)+len(resolved) > dnsAddressLimit {
			return nil, errDNSMessage
		}

		addresses = append(addresses, resolved...)
	}

	if len(addresses) == 0 {
		return nil, errNetworkDenied
	}

	return addresses, nil
}

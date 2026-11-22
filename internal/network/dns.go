package network

import (
	"context"
	"encoding/binary"
	"errors"
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

func (gateway *Gateway) resolveDNS(ctx context.Context, wire []byte, query *dnsmessage.Message) []byte {
	if !gateway.policy.permitsDomain(query.Questions[0].Name.String()) {
		return dnsReply(query, dnsmessage.RCodeRefused, false)
	}

	// Include flags, question case and EDNS options in the cache key; only the ID varies.
	key := string(wire[2:])

	cached := gateway.policy.dns.lookup(key, query.ID)
	if cached != nil {
		return cached
	}

	select {
	case gateway.dnsQueries <- struct{}{}:
		defer func() {
			<-gateway.dnsQueries
		}()
	default:
		return dnsReply(query, dnsmessage.RCodeServerFailure, false)
	}

	upstreams := [2]string{gateway.dns.Primary, gateway.dns.Secondary}

	for _, upstream := range upstreams {
		if upstream == "" {
			continue
		}

		response, message, err := gateway.exchangeDNS(ctx, upstream, wire, query)
		if err != nil {
			continue
		}

		var addresses []netip.Addr

		if gateway.policy.mode == "isolated" && message.RCode == dnsmessage.RCodeSuccess {
			addresses, err = gateway.policy.dnsAddresses(message)
			if errors.Is(err, errNetworkDenied) {
				return dnsReply(query, dnsmessage.RCodeRefused, false)
			}

			if err != nil {
				continue
			}
		}

		entry := dnsCacheRecord(key, response, message, addresses)
		if entry != nil {
			gateway.policy.dns.store(entry)
		}

		return response
	}

	return dnsReply(query, dnsmessage.RCodeServerFailure, false)
}

func (gateway *Gateway) serveDNS(connection net.Conn, stream bool) {
	var (
		buffer [dnsQueryLimit + 1]byte
		size   [2]byte
	)

	for {
		connection.SetDeadline(time.Now().Add(30 * time.Second))

		var (
			length int
			err    error
		)

		if stream {
			_, err = io.ReadFull(connection, size[:])
			if err != nil {
				return
			}

			length = int(binary.BigEndian.Uint16(size[:]))
			if length > dnsQueryLimit {
				return
			}

			_, err = io.ReadFull(connection, buffer[:length])
		} else {
			length, err = connection.Read(buffer[:])
		}

		if err != nil {
			return
		}

		query, parseErr := parseDNSQuery(buffer[:length])
		if parseErr != nil {
			return
		}

		response := gateway.resolveDNS(gateway.ctx, buffer[:length], query)

		if !stream {
			limit := 512

			if len(query.Additionals) != 0 {
				limit = max(512, min(dnsUDPLimit, int(query.Additionals[0].Header.Class)))
			}

			if len(response) > limit {
				response = dnsReply(query, dnsmessage.RCodeSuccess, true)
			}
		} else {
			binary.BigEndian.PutUint16(size[:], uint16(len(response)))

			_, err = connection.Write(size[:])
			if err != nil {
				return
			}
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
		gateway.serveDNS(guest, true)
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
			Questions: []dnsmessage.Question{{Name: name, Type: queryType, Class: dnsmessage.ClassINET}},
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

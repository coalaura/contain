package network

import (
	"bytes"
	"encoding/binary"
	"net/netip"

	"golang.org/x/net/dns/dnsmessage"
)

const dnsRequestConcurrency = 128

// Only validated packets for the gateway's IPv4 DNS address reach this path.
// Datagram-scoped workers avoid retaining idle endpoints or general flow slots,
// and allow concurrent questions from the same source port.
func (gateway *Gateway) forwardDNSPacket(packet []byte, parsed networkPacket) {
	gateway.mutex.Lock()

	if gateway.closed || gateway.ctx.Err() != nil {
		gateway.mutex.Unlock()

		return
	}

	select {
	case gateway.dnsRequests <- struct{}{}:
	default:
		gateway.mutex.Unlock()

		gateway.logger.record(networkEvent{
			protocol:    "DNS",
			action:      "DROP",
			destination: netip.AddrPortFrom(parsed.destination, parsed.port),
			reason:      "request limit",
		})

		return
	}

	port := binary.BigEndian.Uint16(packet[parsed.header:])
	wire := bytes.Clone(packet[parsed.header+8:])

	gateway.workers.Go(func() {
		defer func() {
			<-gateway.dnsRequests
		}()

		query, err := parseDNSQuery(wire)
		if err != nil {
			return
		}

		response := gateway.resolveDNS(gateway.ctx, wire, query)
		limit := 512

		if len(query.Additionals) != 0 {
			limit = max(512, min(dnsUDPLimit, int(query.Additionals[0].Header.Class)))
		}

		if len(response) > limit {
			response = dnsReply(query, dnsmessage.RCodeSuccess, true)
		}

		if gateway.ctx.Err() != nil {
			return
		}

		err = gateway.writeDNSPacket(parsed, port, response)
		if err != nil {
			gateway.transport.Close()
		}
	})

	gateway.mutex.Unlock()
}

func (gateway *Gateway) writeDNSPacket(parsed networkPacket, port uint16, response []byte) error {
	if len(response) > dnsUDPLimit {
		return errDNSMessage
	}

	gateway.writeMutex.Lock()
	defer gateway.writeMutex.Unlock()

	packet := gateway.writeBuffer[2 : 2+28+len(response)]
	clear(packet[:28])
	packet[0] = 0x45
	packet[8] = 64
	packet[9] = 17
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], parsed.destination.AsSlice())
	copy(packet[16:20], parsed.source.AsSlice())
	binary.BigEndian.PutUint16(packet[10:12], networkChecksum(packet[:20], 0))

	payload := packet[20:]
	binary.BigEndian.PutUint16(payload[:2], parsed.port)
	binary.BigEndian.PutUint16(payload[2:4], port)
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(payload)))
	copy(payload[8:], response)

	checksum := networkChecksum(payload, networkPseudoSum(parsed.destination, parsed.source, 17, len(payload)))
	if checksum == 0 {
		checksum = 0xffff
	}

	binary.BigEndian.PutUint16(payload[6:8], checksum)

	return gateway.flushFrame(len(packet))
}

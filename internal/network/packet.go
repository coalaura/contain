package network

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

const (
	networkMTU    = 1280
	networkPort   = 1024
	networkGuest4 = "192.0.2.2"
	networkGuest6 = "fd00:636f:6e74::2"
)

type networkPacketError string

type networkPacket struct {
	source      netip.Addr
	destination netip.Addr
	protocol    byte
	header      int
	port        uint16
	icmpType    byte
	icmpCode    byte
	hasICMP     bool
}

var (
	guestAddress4 = netip.MustParseAddr(networkGuest4)
	guestAddress6 = netip.MustParseAddr(networkGuest6)

	errNetworkPacket = errors.New("unsupported or malformed network packet")
	errNetworkDenied = errors.New("network policy denied destination")
)

func (reason networkPacketError) Error() string {
	return string(reason)
}

func (reason networkPacketError) Is(target error) bool {
	return target == errNetworkPacket
}

func inspectNetworkPacket(packet []byte, policy *networkPolicy) (networkPacket, error) {
	var parsed networkPacket

	if len(packet) < 20 {
		return parsed, networkPacketError("truncated IP header")
	}

	if len(packet) > networkMTU {
		return parsed, networkPacketError("packet exceeds MTU")
	}

	switch packet[0] >> 4 {
	case 4:
		parsed.source = netip.AddrFrom4([4]byte(packet[12:16]))
		parsed.destination = netip.AddrFrom4([4]byte(packet[16:20]))
		parsed.protocol = packet[9]
		parsed.header = 20

		headerLength := int(packet[0]&15) * 4
		if headerLength < 20 || headerLength > len(packet) {
			return parsed, networkPacketError("invalid IPv4 header length")
		}

		if headerLength != 20 {
			return parsed, networkPacketError("IPv4 options unsupported")
		}

		if int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) {
			return parsed, networkPacketError("invalid IPv4 packet length")
		}

		fragment := binary.BigEndian.Uint16(packet[6:8])
		if fragment&0x8000 != 0 {
			return parsed, networkPacketError("invalid IPv4 flags")
		}

		// Reject first fragments too; the gateway does not reassemble packets.
		if fragment&0x3fff != 0 {
			return parsed, networkPacketError("IPv4 fragments unsupported")
		}

		if packet[8] == 0 {
			return parsed, networkPacketError("zero IPv4 TTL")
		}

		if networkChecksum(packet[:20], 0) != 0 {
			return parsed, networkPacketError("invalid IPv4 checksum")
		}
	case 6:
		if len(packet) < 40 {
			return parsed, networkPacketError("truncated IPv6 header")
		}

		parsed.source = netip.AddrFrom16([16]byte(packet[8:24]))
		parsed.destination = netip.AddrFrom16([16]byte(packet[24:40]))
		parsed.protocol = packet[6]
		parsed.header = 40

		if int(binary.BigEndian.Uint16(packet[4:6]))+40 != len(packet) {
			return parsed, networkPacketError("invalid IPv6 packet length")
		}

		if packet[7] == 0 {
			return parsed, networkPacketError("zero IPv6 hop limit")
		}
	default:
		return parsed, networkPacketError("unsupported IP version")
	}

	payload := packet[parsed.header:]
	if (parsed.protocol == 1 || parsed.protocol == 58) && len(payload) >= 2 {
		parsed.icmpType = payload[0]
		parsed.icmpCode = payload[1]
		parsed.hasICMP = true
	}

	if parsed.source != guestAddress4 && parsed.source != guestAddress6 {
		return parsed, networkPacketError("unexpected source address")
	}

	if parsed.destination.Is4In6() {
		return parsed, networkPacketError("IPv4-mapped IPv6 destination unsupported")
	}

	if parsed.destination.IsMulticast() {
		return parsed, networkPacketError("multicast destination unsupported")
	}

	if parsed.destination.Is6() && parsed.destination.IsLinkLocalUnicast() {
		return parsed, networkPacketError("link-local IPv6 destination unsupported")
	}

	if !networkDestination(parsed.destination) {
		return parsed, networkPacketError("unspecified or broadcast destination")
	}

	pseudo := networkPseudoSum(parsed.source, parsed.destination, parsed.protocol, len(payload))

	switch parsed.protocol {
	case 6:
		if len(payload) < 20 || int(payload[12]>>4)*4 < 20 || int(payload[12]>>4)*4 > len(payload) {
			return parsed, networkPacketError("invalid TCP header length")
		}

		parsed.port = binary.BigEndian.Uint16(payload[2:4])

		if networkChecksum(payload, pseudo) != 0 {
			return parsed, networkPacketError("invalid TCP checksum")
		}
	case 17:
		if len(payload) < 8 || int(binary.BigEndian.Uint16(payload[4:6])) != len(payload) {
			return parsed, networkPacketError("invalid UDP length")
		}

		parsed.port = binary.BigEndian.Uint16(payload[2:4])
		checksum := binary.BigEndian.Uint16(payload[6:8])

		if parsed.source.Is6() && checksum == 0 || checksum != 0 && networkChecksum(payload, pseudo) != 0 {
			return parsed, networkPacketError("invalid UDP checksum")
		}
	case 1, 58:
		if parsed.protocol == 1 && !parsed.source.Is4() || parsed.protocol == 58 && !parsed.source.Is6() {
			return parsed, networkPacketError("ICMP IP version mismatch")
		}

		if len(payload) < 8 {
			return parsed, networkPacketError("truncated ICMP header")
		}

		echoType := byte(128)

		if parsed.protocol == 1 {
			echoType = 8
			pseudo = 0
		}

		if parsed.icmpType != echoType || parsed.icmpCode != 0 {
			return parsed, networkPacketError("ICMP type or code unsupported")
		}

		if networkChecksum(payload, pseudo) != 0 {
			return parsed, networkPacketError("invalid ICMP checksum")
		}
	default:
		// Includes IPv6 extension headers, IP-in-IP, GRE, ESP and SCTP.
		return parsed, networkPacketError("IP protocol or extension header unsupported")
	}

	if (parsed.protocol == 6 || parsed.protocol == 17) && (parsed.port == 0 || binary.BigEndian.Uint16(payload[:2]) == 0) {
		return parsed, networkPacketError("zero TCP or UDP port")
	}

	// Only netstack connections opened by published ports may receive these replies.
	// The TCP/UDP fallback handlers reject this address, so it cannot authorize egress.
	if parsed.destination == publishAddress && policy.mode != "closed" && (parsed.protocol == 6 || parsed.protocol == 17) {
		return parsed, nil
	}

	if parsed.protocol == 17 && parsed.port == 443 && policy.mode != "open" {
		return parsed, errNetworkDenied
	}

	destination := netip.AddrPortFrom(parsed.destination, parsed.port)
	if !policy.servesDNS(destination) && !policy.permits(parsed.destination) {
		return parsed, errNetworkDenied
	}

	return parsed, nil
}

func networkDestination(address netip.Addr) bool {
	scoped := address.Is6() && address.IsLinkLocalUnicast()

	return address.IsValid() && !address.IsUnspecified() && !address.IsMulticast() && !scoped && address != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

func networkPseudoSum(source, destination netip.Addr, protocol byte, length int) uint32 {
	sum := uint32(protocol) + uint32(length)
	sum += uint32(^networkChecksum(source.AsSlice(), 0))
	sum += uint32(^networkChecksum(destination.AsSlice(), 0))

	return sum
}

func networkChecksum(data []byte, sum uint32) uint16 {
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}

	if len(data) != 0 {
		sum += uint32(data[0]) << 8
	}

	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}

	return ^uint16(sum)
}

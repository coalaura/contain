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

type networkPacket struct {
	source      netip.Addr
	destination netip.Addr
	protocol    byte
	header      int
	port        uint16
}

var (
	guestAddress4 = netip.MustParseAddr(networkGuest4)
	guestAddress6 = netip.MustParseAddr(networkGuest6)

	errNetworkPacket = errors.New("unsupported or malformed network packet")
	errNetworkDenied = errors.New("network policy denied destination")
)

func inspectNetworkPacket(packet []byte, policy *networkPolicy) (networkPacket, error) {
	var parsed networkPacket

	if len(packet) < 20 || len(packet) > networkMTU {
		return parsed, errNetworkPacket
	}

	switch packet[0] >> 4 {
	case 4:
		// Options and fragments are deliberately unsupported, including first fragments.
		if packet[0] != 0x45 || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) || binary.BigEndian.Uint16(packet[6:8])&0xbfff != 0 || packet[8] == 0 || networkChecksum(packet[:20], 0) != 0 {
			return parsed, errNetworkPacket
		}

		parsed.source = netip.AddrFrom4([4]byte(packet[12:16]))
		parsed.destination = netip.AddrFrom4([4]byte(packet[16:20]))
		parsed.protocol = packet[9]
		parsed.header = 20

		if parsed.source != guestAddress4 {
			return parsed, errNetworkPacket
		}
	case 6:
		if len(packet) < 40 || int(binary.BigEndian.Uint16(packet[4:6]))+40 != len(packet) || packet[7] == 0 {
			return parsed, errNetworkPacket
		}

		parsed.source = netip.AddrFrom16([16]byte(packet[8:24]))
		parsed.destination = netip.AddrFrom16([16]byte(packet[24:40]))
		parsed.protocol = packet[6]
		parsed.header = 40

		if parsed.source != guestAddress6 || parsed.destination.Is4In6() {
			return parsed, errNetworkPacket
		}
	default:
		return parsed, errNetworkPacket
	}

	if !networkDestination(parsed.destination) {
		return parsed, errNetworkPacket
	}

	payload := packet[parsed.header:]
	pseudo := networkPseudoSum(parsed.source, parsed.destination, parsed.protocol, len(payload))

	switch parsed.protocol {
	case 6:
		if len(payload) < 20 || int(payload[12]>>4)*4 < 20 || int(payload[12]>>4)*4 > len(payload) || networkChecksum(payload, pseudo) != 0 {
			return parsed, errNetworkPacket
		}

		parsed.port = binary.BigEndian.Uint16(payload[2:4])
	case 17:
		if len(payload) < 8 || int(binary.BigEndian.Uint16(payload[4:6])) != len(payload) {
			return parsed, errNetworkPacket
		}

		checksum := binary.BigEndian.Uint16(payload[6:8])
		if parsed.source.Is6() && checksum == 0 || checksum != 0 && networkChecksum(payload, pseudo) != 0 {
			return parsed, errNetworkPacket
		}

		parsed.port = binary.BigEndian.Uint16(payload[2:4])
		if parsed.port == 443 && policy.mode != "open" {
			return parsed, errNetworkPacket
		}
	case 1:
		if !parsed.source.Is4() || len(payload) < 8 || payload[0] != 8 || payload[1] != 0 || networkChecksum(payload, 0) != 0 {
			return parsed, errNetworkPacket
		}
	case 58:
		if !parsed.source.Is6() || len(payload) < 8 || payload[0] != 128 || payload[1] != 0 || networkChecksum(payload, pseudo) != 0 {
			return parsed, errNetworkPacket
		}
	default:
		// Includes IPv6 extension headers, IP-in-IP, GRE, ESP and SCTP.
		return parsed, errNetworkPacket
	}

	if (parsed.protocol == 6 || parsed.protocol == 17) && (parsed.port == 0 || binary.BigEndian.Uint16(payload[:2]) == 0) {
		return parsed, errNetworkPacket
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

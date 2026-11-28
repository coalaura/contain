package network

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type Port struct {
	Host     netip.AddrPort
	Guest    uint16
	Protocol string
}

func ResolvePorts(values []string) ([]Port, error) {
	if len(values) > networkConnections {
		return nil, fmt.Errorf("machine.network.ports: at most %d published ports are supported", networkConnections)
	}

	ports := make([]Port, 0, len(values))

	for index, value := range values {
		port, err := parsePort(value)
		if err != nil {
			return nil, fmt.Errorf("machine.network.ports[%d] %q: %w", index, value, err)
		}

		for _, previous := range ports {
			if previous.Protocol != port.Protocol || previous.Host.Port() != port.Host.Port() {
				continue
			}

			address := port.Host.Addr()
			other := previous.Host.Addr()

			if address.Is4() == other.Is4() && (address == other || address.IsUnspecified() || other.IsUnspecified()) {
				return nil, fmt.Errorf("machine.network.ports[%d] %q: overlapping host binding %s/%s", index, value, previous.Host, port.Protocol)
			}
		}

		ports = append(ports, port)
	}

	return ports, nil
}

func parsePort(value string) (Port, error) {
	var port Port

	binding, protocol, explicit := strings.Cut(value, "/")
	if !explicit {
		protocol = "tcp"
	}

	if protocol != "tcp" && protocol != "udp" {
		return port, fmt.Errorf("protocol must be tcp or udp")
	}

	host, guestPort, found := strings.CutLast(binding, ":")
	if !found {
		return port, fmt.Errorf("expected [HOST_IP:]HOST:GUEST[/tcp|udp]")
	}

	guest, err := parsePortNumber(guestPort)
	if err != nil {
		return port, fmt.Errorf("guest port: %w", err)
	}

	if !strings.ContainsRune(host, ':') {
		host = "127.0.0.1:" + host
	}

	address, err := netip.ParseAddrPort(host)
	if err != nil || address.Port() == 0 {
		return port, fmt.Errorf("host must be an IP address and port (1-65535); bracket IPv6 addresses")
	}

	address = netip.AddrPortFrom(address.Addr().Unmap(), address.Port())
	if address.Addr().Zone() != "" || address.Addr().IsMulticast() || address.Addr().IsLinkLocalUnicast() || address.Addr() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return port, fmt.Errorf("host must be a local unicast or wildcard IP address")
	}

	return Port{Host: address, Guest: guest, Protocol: protocol}, nil
}

func parsePortNumber(value string) (uint16, error) {
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("must be a number from 1 to 65535")
		}
	}

	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("must be a number from 1 to 65535")
	}

	return uint16(port), nil
}

package network

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"
)

const networkClientHelloLimit = 64 * 1024

type networkTLSReplay struct {
	net.Conn
	pending []byte
}

type networkHelloFields []byte

var errNetworkClientHello = errors.New("invalid or oversized TLS ClientHello")

func (connection *networkTLSReplay) Read(data []byte) (int, error) {
	if len(connection.pending) == 0 {
		return connection.Conn.Read(data)
	}

	count := copy(data, connection.pending)
	connection.pending = connection.pending[count:]

	if len(connection.pending) == 0 {
		connection.pending = nil
	}

	return count, nil
}

func (fields *networkHelloFields) take(length int) []byte {
	if length > len(*fields) {
		return nil
	}

	value := (*fields)[:length]
	*fields = (*fields)[length:]

	return value
}

func (fields *networkHelloFields) vector(width int) []byte {
	prefix := fields.take(width)
	if prefix == nil {
		return nil
	}

	length := int(prefix[0])

	if width == 2 {
		length = int(binary.BigEndian.Uint16(prefix))
	}

	return fields.take(length)
}

func (gateway *Gateway) routeHTTPS(guest, upstream net.Conn, destination netip.AddrPort) {
	guest.SetReadDeadline(time.Now().Add(networkDialTimeout))

	buffered, hostname, err := readNetworkClientHello(guest)
	if err != nil {
		gateway.logger.record(networkEvent{protocol: "TLS", action: "ERROR", destination: destination, reason: "ClientHello inspection failed"})

		return
	}

	guest.SetReadDeadline(time.Time{})

	if gateway.credentials.intercepts(hostname) {
		connection := &networkTLSReplay{Conn: guest, pending: buffered}
		gateway.interceptHTTPS(connection, upstream, destination)

		return
	}

	// Flush the captured records before relaying, keeping the original connections
	// (and their half-close support) on the transparent path.
	upstream.SetWriteDeadline(time.Now().Add(networkDialTimeout))

	_, err = upstream.Write(buffered)
	if err != nil {
		return
	}

	upstream.SetWriteDeadline(time.Time{})

	relayNetwork(guest, upstream, networkIdle)
}

func readNetworkClientHello(reader io.Reader) ([]byte, []byte, error) {
	buffered := make([]byte, 0, 4096)

	var (
		hello      []byte
		fragmented bool
		err        error
	)

	for {
		offset := len(buffered)

		buffered, err = readNetworkTLSBytes(reader, buffered, 5)
		if err != nil {
			return nil, nil, err
		}

		header := buffered[offset:]
		length := int(binary.BigEndian.Uint16(header[3:]))

		if header[0] != 22 || header[1] != 3 || length == 0 || length > 16384 {
			return nil, nil, errNetworkClientHello
		}

		buffered, err = readNetworkTLSBytes(reader, buffered, length)
		if err != nil {
			return nil, nil, err
		}

		payload := buffered[offset+5:]

		if offset == 0 {
			// The usual single-record hello is parsed directly in the replay buffer.
			hello = payload
		} else {
			if !fragmented {
				// Only fragmented handshakes need a separate contiguous parsing buffer.
				assembled := make([]byte, len(hello), max(4096, len(hello)+len(payload)))
				copy(assembled, hello)

				hello = assembled
				fragmented = true
			}

			hello = append(hello, payload...)
		}

		if hello[0] != 1 {
			return nil, nil, errNetworkClientHello
		}

		if len(hello) < 4 {
			continue
		}

		length = 4 + int(hello[1])<<16 + int(hello[2])<<8 + int(hello[3])
		if length > networkClientHelloLimit-5 {
			return nil, nil, errNetworkClientHello
		}

		if len(hello) < length {
			continue
		}

		hostname, err := networkClientHelloName(hello[4:length])

		return buffered, hostname, err
	}
}

func readNetworkTLSBytes(reader io.Reader, buffered []byte, count int) ([]byte, error) {
	length := len(buffered)
	needed := length + count

	if needed > networkClientHelloLimit {
		return nil, errNetworkClientHello
	}

	if needed > cap(buffered) {
		capacity := min(networkClientHelloLimit, max(needed, cap(buffered)*2))
		grown := make([]byte, length, capacity)
		copy(grown, buffered)

		buffered = grown
	}

	buffered = buffered[:needed]
	_, err := io.ReadFull(reader, buffered[length:])

	return buffered, err
}

func networkClientHelloName(hello networkHelloFields) ([]byte, error) {
	if hello.take(34) == nil {
		return nil, errNetworkClientHello
	}

	session := hello.vector(1)
	if session == nil || len(session) > 32 {
		return nil, errNetworkClientHello
	}

	ciphers := hello.vector(2)
	if len(ciphers) == 0 || len(ciphers)%2 != 0 {
		return nil, errNetworkClientHello
	}

	compression := hello.vector(1)
	if len(compression) == 0 {
		return nil, errNetworkClientHello
	}

	if len(hello) == 0 {
		return nil, nil
	}

	extensions := networkHelloFields(hello.vector(2))
	if extensions == nil || len(hello) != 0 {
		return nil, errNetworkClientHello
	}

	var (
		hostname []byte
		seenName bool
	)

	for len(extensions) > 0 {
		kind := extensions.take(2)
		if kind == nil {
			return nil, errNetworkClientHello
		}

		value := extensions.vector(2)
		if value == nil {
			return nil, errNetworkClientHello
		}

		if binary.BigEndian.Uint16(kind) != 0 {
			continue
		}

		if seenName {
			return nil, errNetworkClientHello
		}

		seenName = true

		names := networkHelloFields(value)
		list := networkHelloFields(names.vector(2))

		if len(list) == 0 || len(names) != 0 {
			return nil, errNetworkClientHello
		}

		for len(list) > 0 {
			nameType := list.take(1)
			name := list.vector(2)

			if len(name) == 0 {
				return nil, errNetworkClientHello
			}

			if nameType[0] != 0 {
				continue
			}

			if hostname != nil || len(name) > 253 {
				return nil, errNetworkClientHello
			}

			hostname = name
		}
	}

	return hostname, nil
}

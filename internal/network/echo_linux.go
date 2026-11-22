package network

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
)

func sendNetworkEcho(ctx context.Context, destination netip.Addr, message []byte) error {
	protocol := "udp4"
	local := "0.0.0.0"
	expected := byte(0)

	if destination.Is6() {
		protocol = "udp6"
		local = "::"
		expected = 129
	}

	connection, err := icmp.ListenPacket(protocol, local)
	if err != nil {
		return err
	}

	defer connection.Close()

	stop := context.AfterFunc(ctx, func() {
		connection.Close()
	})

	defer stop()

	connection.SetDeadline(time.Now().Add(3*time.Second))

	_, err = connection.WriteTo(message, &net.UDPAddr{IP: destination.AsSlice()})
	if err != nil {
		return err
	}

	var reply [networkMTU]byte

	count, peer, err := connection.ReadFrom(reply[:])
	if err != nil {
		return err
	}

	address, valid := peer.(*net.UDPAddr)
	if !valid || !address.IP.Equal(destination.AsSlice()) || count != len(message) || reply[0] != expected || reply[1] != 0 || !bytes.Equal(reply[6:count], message[6:]) {
		return errors.New("unexpected ICMP echo reply")
	}

	return nil
}

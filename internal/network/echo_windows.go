package network

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	networkIPHelper    = windows.NewLazySystemDLL("iphlpapi.dll")
	networkICMPCreate  = networkIPHelper.NewProc("IcmpCreateFile")
	networkICMP6Create = networkIPHelper.NewProc("Icmp6CreateFile")
	networkICMPClose   = networkIPHelper.NewProc("IcmpCloseHandle")
	networkICMPSend    = networkIPHelper.NewProc("IcmpSendEcho")
	networkICMP6Send   = networkIPHelper.NewProc("Icmp6SendEcho2")
)

func sendNetworkEcho(ctx context.Context, destination netip.Addr, message []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	create := networkICMPCreate

	if destination.Is6() {
		create = networkICMP6Create
	}

	handle, _, err := create.Call()
	if handle == uintptr(windows.InvalidHandle) {
		return err
	}

	defer networkICMPClose.Call(handle)

	// IP Helper generates the host echo header. Only the bounded guest payload is sent.
	var (
		request [networkMTU]byte
		reply   [networkMTU + 128]byte
	)

	copy(request[:], message[8:])
	requestSize := uintptr(len(message) - 8)
	statusOffset := 4

	var count uintptr

	if destination.Is4() {
		address := destination.As4()

		count, _, err = networkICMPSend.Call(handle, uintptr(binary.LittleEndian.Uint32(address[:])), uintptr(unsafe.Pointer(&request[0])), requestSize, 0, uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), 3000)
	} else {
		source := windows.RawSockaddrInet6{Family: windows.AF_INET6}
		target := windows.RawSockaddrInet6{Family: windows.AF_INET6, Addr: destination.As16()}

		count, _, err = networkICMP6Send.Call(handle, 0, 0, 0, uintptr(unsafe.Pointer(&source)), uintptr(unsafe.Pointer(&target)), uintptr(unsafe.Pointer(&request[0])), requestSize, 0, uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), 3000)

		// ICMPV6_ECHO_REPLY follows a 28-byte IPV6_ADDRESS_EX, not sockaddr_in6.
		statusOffset = 28
	}

	if count == 0 {
		return err
	}

	if binary.LittleEndian.Uint32(reply[statusOffset:statusOffset+4]) != 0 {
		return errors.New("host ICMP echo failed")
	}

	return ctx.Err()
}

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	guestNetworkMTU  = 1280
	guestNetworkPort = 1024
)

type guestNetwork struct {
	tun       *os.File
	transport *os.File
	mutex     sync.Mutex
}

func (network *guestNetwork) writeFrame(packet []byte) error {
	network.mutex.Lock()
	defer network.mutex.Unlock()

	var frame [guestNetworkMTU + 2]byte

	binary.BigEndian.PutUint16(frame[:2], uint16(len(packet)))
	copy(frame[2:], packet)

	network.transport.SetWriteDeadline(time.Now().Add(10 * time.Second))

	_, err := network.transport.Write(frame[:len(packet)+2])
	return err
}

func (network *guestNetwork) readPackets() error {
	var (
		frame [guestNetworkMTU]byte
		size  [2]byte
	)

	for {
		network.transport.SetReadDeadline(time.Now().Add(20 * time.Second))

		_, err := io.ReadFull(network.transport, size[:])
		if err != nil {
			return err
		}

		length := int(binary.BigEndian.Uint16(size[:]))
		if length > len(frame) {
			return errors.New("oversize gateway packet")
		}

		if length == 0 {
			continue
		}

		_, err = io.ReadFull(network.transport, frame[:length])
		if err != nil {
			return err
		}

		_, err = network.tun.Write(frame[:length])
		if err != nil {
			return err
		}
	}
}

func (network *guestNetwork) writePackets() error {
	var packet [guestNetworkMTU + 1]byte

	for {
		count, err := network.tun.Read(packet[:])
		if err != nil {
			return err
		}

		if count > guestNetworkMTU {
			return errors.New("oversize guest packet")
		}

		err = network.writeFrame(packet[:count])
		if err != nil {
			return err
		}
	}
}

func runNetworkHelper(serving bool) error {
	if !serving {
		return startGuestService("--serve-network")
	}

	ready := os.NewFile(3, "ready")
	defer ready.Close()

	err := serveGuestNetwork(ready)
	if err != nil {
		fmt.Fprintln(ready, err)
	}

	return err
}

func serveGuestNetwork(ready *os.File) error {
	transport, err := connectGuestNetwork()
	if err != nil {
		return fmt.Errorf("connect vsock gateway (requires virtio-vsock): %w", err)
	}

	defer transport.Close()

	transport.SetDeadline(time.Now().Add(10 * time.Second))

	_, err = io.WriteString(transport, "CONTAIN1")
	if err != nil {
		return err
	}

	var handshake [8]byte

	_, err = io.ReadFull(transport, handshake[:])
	if err != nil || string(handshake[:]) != "CONTAIN1" {
		return errors.New("network gateway handshake failed")
	}

	tun, err := openGuestTUN()
	if err != nil {
		return fmt.Errorf("create contain0 (requires the tun module and /dev/net/tun): %w", err)
	}

	defer tun.Close()

	err = configureGuestNetwork()
	if err != nil {
		return err
	}

	network := &guestNetwork{tun: tun, transport: transport}
	results := make(chan error, 2)

	go func() {
		results <- network.writePackets()
	}()

	go func() {
		results <- network.readPackets()
	}()

	_, err = io.WriteString(ready, "ready\n")
	ready.Close()

	if err != nil {
		transport.Close()
		tun.Close()

		<-results
		<-results

		return err
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err = <-results:
			transport.Close()
			tun.Close()

			<-results

			return err
		case <-ticker.C:
			err = network.writeFrame(nil)
			if err != nil {
				transport.Close()
				tun.Close()

				<-results
				<-results

				return err
			}
		}
	}
}

func connectGuestNetwork() (*os.File, error) {
	exec.Command("modprobe", "vmw_vsock_virtio_transport").Run()

	descriptor, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}

	err = unix.Connect(descriptor, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: guestNetworkPort})
	if errors.Is(err, unix.EINPROGRESS) {
		poll := []unix.PollFd{{Fd: int32(descriptor), Events: unix.POLLOUT}}

		_, err = unix.Poll(poll, 10000)
		if err == nil {
			var status int

			status, err = unix.GetsockoptInt(descriptor, unix.SOL_SOCKET, unix.SO_ERROR)
			if err == nil && status != 0 {
				err = unix.Errno(status)
			}

			if err == nil && poll[0].Revents&unix.POLLOUT == 0 {
				err = errors.New("vsock connect timed out")
			}
		}
	}

	if err != nil {
		unix.Close(descriptor)

		return nil, err
	}

	return os.NewFile(uintptr(descriptor), "gateway-vsock"), nil
}

func openGuestTUN() (*os.File, error) {
	exec.Command("modprobe", "tun").Run()

	descriptor, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}

	request, err := unix.NewIfreq("contain0")
	if err != nil {
		unix.Close(descriptor)

		return nil, err
	}

	request.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)

	err = unix.IoctlIfreq(descriptor, unix.TUNSETIFF, request)
	if err != nil {
		unix.Close(descriptor)

		return nil, err
	}

	// Attach before registering with Go's poller: an unattached TUN reports POLLERR.
	return os.NewFile(uintptr(descriptor), "contain-tun"), nil
}

func configureGuestNetwork() error {
	// The point-to-point gateway has no neighbor discovery. BusyBox ip lacks nodad,
	// so disable duplicate-address detection before adding the fixed IPv6 address.
	err := os.WriteFile("/proc/sys/net/ipv6/conf/contain0/accept_dad", []byte("0\n"), 0644)
	if err != nil {
		return fmt.Errorf("disable IPv6 duplicate-address detection on contain0: %w", err)
	}

	commands := [][]string{
		{"ip", "link", "set", "contain0", "mtu", "1280", "up"},
		{"ip", "addr", "add", "192.0.2.2/32", "dev", "contain0"},
		{"ip", "-6", "addr", "add", "fd00:636f:6e74::2/128", "dev", "contain0"},
		{"ip", "route", "replace", "default", "dev", "contain0"},
		{"ip", "-6", "route", "replace", "default", "dev", "contain0"},
	}

	for _, arguments := range commands {
		output, err := exec.Command(arguments[0], arguments[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("configure guest network (%s): %w: %s", strings.Join(arguments, " "), err, output)
		}
	}

	return os.WriteFile("/etc/resolv.conf", []byte("nameserver 192.0.2.1\noptions timeout:5 attempts:2\n"), 0644)
}

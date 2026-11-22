package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

const guestResizeCommand = "/run/contain-resize"

type guestNetworkStep struct {
	name string
	command string
}

var guestNetworkSteps = []guestNetworkStep{
	{name: "check image tools", command: "command -v update-ca-certificates >/dev/null && command -v ip >/dev/null || { printf '%s\\n' 'Image requires ca-certificates and an IPv6-capable ip command (BusyBox or iproute2).'; false; }"},
	{name: "mount helper export", command: "mkdir -p /run/contain-guest && mount -t virtiofs -o ro contain_guest /run/contain-guest"},
	{name: "copy guest helper", command: "cp /run/contain-guest/resize /run/contain-resize && chmod 700 /run/contain-resize"},
	{name: "install public CA", command: "mkdir -p /usr/local/share/ca-certificates && cp /run/contain-guest/contain.crt /usr/local/share/ca-certificates/contain.crt"},
	{name: "unmount helper export", command: "umount /run/contain-guest && rmdir /run/contain-guest"},
	{name: "update guest trust store", command: "update-ca-certificates && export NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/contain.crt"},
	{name: "start vsock/TUN helper", command: "/run/contain-resize --network"},
}

//go:generate go run ./guest/generate.go

//go:embed guest/resize
var guestResizeBinary []byte

func initializeGuestNetwork(ctx context.Context, con *console) error {
	for index := range guestNetworkSteps {
		step := &guestNetworkSteps[index]

		err := con.runShell(ctx, step.command)
		if err != nil {
			return fmt.Errorf("initialize guest networking: %s: %w", step.name, err)
		}
	}

	return nil
}

func prepareGuestHelper(publicCA []byte) (string, func(), error) {
	directory, err := os.MkdirTemp(paths.DataDirectory, "contain-guest-*")
	if err != nil {
		return "", nil, fmt.Errorf("create guest helper directory: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(directory)
	}

	err = os.WriteFile(filepath.Join(directory, "resize"), guestResizeBinary, 0600)
	if err != nil {
		cleanup()

		return "", nil, fmt.Errorf("write guest helper: %w", err)
	}

	err = os.WriteFile(filepath.Join(directory, "contain.crt"), publicCA, 0644)
	if err != nil {
		cleanup()

		return "", nil, fmt.Errorf("write guest CA certificate: %w", err)
	}

	return directory, cleanup, nil
}

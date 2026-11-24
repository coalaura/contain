package main

import (
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	guestResizeCommand  = "/run/contain-resize"
	guestHelperCommand  = "mkdir -p /run/contain-guest && mount -t virtiofs -o ro contain_guest /run/contain-guest && cp /run/contain-guest/mount.sh /run/contain-mount.sh"
	guestNetworkCommand = "bash /run/contain-guest/network.sh && umount /run/contain-guest && rmdir /run/contain-guest && export NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/contain.crt"
)

type guestFile struct {
	name    string
	content []byte
	mode    fs.FileMode
}

//go:embed network_guest.sh
var guestNetworkScript []byte

//go:generate go run ./guest/generate.go

//go:embed guest/resize
var guestResizeBinary []byte

func initializeGuestNetwork(ctx context.Context, con *console) error {
	err := con.runShell(ctx, guestNetworkCommand)
	if err != nil {
		return fmt.Errorf("initialize guest networking: %w", err)
	}

	return nil
}

func prepareGuestHelper(imageDirectory string, publicCA []byte) (string, func(), error) {
	directory, err := os.MkdirTemp(imageDirectory, "contain-guest-*")
	if err != nil {
		return "", nil, fmt.Errorf("create guest helper directory: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(directory)
	}

	files := [...]guestFile{
		{name: "resize", content: guestResizeBinary, mode: 0600},
		{name: "contain.crt", content: publicCA, mode: 0644},
		{name: "network.sh", content: guestNetworkScript, mode: 0600},
		{name: "mount.sh", content: guestMountScript, mode: 0600},
		{name: "shutdown.sh", content: guestShutdownSetupScript, mode: 0600},
	}

	for _, file := range files {
		err = os.WriteFile(filepath.Join(directory, file.name), file.content, file.mode)
		if err != nil {
			cleanup()

			return "", nil, fmt.Errorf("write guest helper file %s: %w", file.name, err)
		}
	}

	return directory, cleanup, nil
}

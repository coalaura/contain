package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

const guestResizeCommand = "mkdir -p /run/contain-guest && mount -t virtiofs -o ro contain_guest /run/contain-guest && cp /run/contain-guest/resize /run/contain-resize && chmod 700 /run/contain-resize && umount /run/contain-guest && rmdir /run/contain-guest && /run/contain-resize"

//go:generate go run ./guest/generate.go

//go:embed guest/resize
var guestResizeBinary []byte

func prepareGuestHelper() (string, func(), error) {
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

	return directory, cleanup, nil
}

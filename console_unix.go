//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func consoleAddress() (string, func(), error) {
	directory, err := os.MkdirTemp("", "contain-")
	if err != nil {
		return "", nil, fmt.Errorf("create console directory: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(directory)
	}

	address := filepath.Join(directory, "console.sock")
	if len(address) > 103 {
		cleanup()

		return "", nil, fmt.Errorf("console socket path is too long: %q; use a shorter TMPDIR", address)
	}

	return address, cleanup, nil
}

func dialConsole(ctx context.Context, address string) (consoleConn, error) {
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}

	return dialer.DialContext(ctx, "unix", address)
}

func consoleDisconnected(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

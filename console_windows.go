package main

import (
	"context"
	"crypto/rand"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func consoleAddress() (string, func(), error) {
	return `\\.\pipe\contain-` + rand.Text(), func() {}, nil
}

func dialConsole(ctx context.Context, address string) (consoleConn, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	name, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return nil, err
	}

	flags := uint32(windows.FILE_FLAG_OVERLAPPED | windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IDENTIFICATION)

	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}

	// Overlapped pipe handles let os.File use the runtime poller for deadlines
	// and interrupt pending reads and writes when the console is closed.
	return os.NewFile(uintptr(handle), address), nil
}

func consoleDisconnected(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) || errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED)
}

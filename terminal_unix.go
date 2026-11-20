//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func relayInput(ctx context.Context, output io.Writer) error {
	var buffer [4096]byte

	poll := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		ready, err := unix.Poll(poll, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return err
		}

		if ready == 0 {
			continue
		}

		count, err := os.Stdin.Read(buffer[:])
		if count > 0 {
			_, writeErr := output.Write(buffer[:count])
			if writeErr != nil {
				return writeErr
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}
}

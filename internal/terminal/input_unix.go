//go:build !windows

package terminal

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func RelayInput(ctx context.Context, output io.Writer, resize *Resize) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}

	defer reader.Close()
	defer writer.Close()

	canceled := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		writer.Write([]byte{1})

		close(canceled)
	})

	defer func() {
		if !stop() {
			<-canceled
		}
	}()

	var buffer [4096]byte

	// A cancellation pipe wakes the blocking wait without periodic timeouts.
	poll := []unix.PollFd{
		{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN},
		{Fd: int32(reader.Fd()), Events: unix.POLLIN},
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, err := unix.Poll(poll, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return err
		}

		if ctx.Err() != nil {
			return ctx.Err()
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

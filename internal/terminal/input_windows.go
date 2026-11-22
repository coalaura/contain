package terminal

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var cancelSynchronousIO = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

func RelayInput(ctx context.Context, output io.Writer, resize *Resize) error {
	input := windows.Handle(os.Stdin.Fd())

	var mode uint32

	err := windows.GetConsoleMode(input, &mode)
	if err == nil {
		return relayConsoleInput(ctx, input, output, resize)
	}

	if resize == nil {
		return relayStreamInput(ctx, output)
	}

	return relayRedirectedInput(ctx, output, resize)
}

func relayRedirectedInput(ctx context.Context, output io.Writer, resize *Resize) error {
	// Redirecting stdin does not detach the console. Keep receiving its resize
	// events, but take guest input exclusively from the redirected stream.
	input, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return err
	}

	defer input.Close()

	inputCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		streamErr := relayStreamInput(inputCtx, output)
		done <- streamErr

		if streamErr != nil {
			cancel()
		}
	}()

	err = relayConsoleInput(inputCtx, windows.Handle(input.Fd()), io.Discard, resize)

	cancel()

	streamErr := <-done

	if errors.Is(err, context.Canceled) {
		err = nil
	}

	if errors.Is(streamErr, context.Canceled) {
		streamErr = nil
	}

	return errors.Join(err, streamErr)
}

func relayStreamInput(ctx context.Context, output io.Writer) error {
	// Redirected reads can be synchronous. Pin their thread so cancellation cannot
	// interrupt unrelated I/O, and finish cancellation before releasing it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var thread windows.Handle

	process := windows.CurrentProcess()

	err := windows.DuplicateHandle(process, windows.CurrentThread(), process, &thread, 0, false, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		return err
	}

	defer windows.CloseHandle(thread)

	done := make(chan struct{})
	canceled := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		defer close(canceled)

		// Redirected stdin may use the runtime poller instead of synchronous I/O.
		os.Stdin.SetReadDeadline(time.Now())

		// Retry to cover cancellation between the context check and Read.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			cancelSynchronousIO.Call(uintptr(thread))

			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	})

	defer func() {
		close(done)

		if !stop() {
			<-canceled
		}

		os.Stdin.SetReadDeadline(time.Time{})
	}()

	var buffer [4096]byte

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		count, err := os.Stdin.Read(buffer[:])

		if ctx.Err() != nil {
			return ctx.Err()
		}

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

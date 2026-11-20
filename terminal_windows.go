package main

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

func relayInput(ctx context.Context, output io.Writer) error {
	// Console reads are synchronous. Pin their thread so cancellation cannot
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

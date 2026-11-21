package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/term"
)

func relayTerminal(ctx context.Context, con *console, exited <-chan struct{}, resize *terminalResize) error {
	fd := int(os.Stdin.Fd())

	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}

		defer term.Restore(fd, state)
	}

	inputCtx, stopInput := context.WithCancel(ctx)
	inputDone := make(chan error, 1)

	go func() {
		err := relayInput(inputCtx, con.Writer(), resize)
		inputDone <- err

		if err != nil && inputCtx.Err() == nil {
			con.Close()
		}
	}()

	watchDone := make(chan struct{})
	stopWatch := make(chan struct{})

	go func() {
		defer close(watchDone)

		select {
		case <-ctx.Done():
			con.Close()
		case <-exited:
			// Drain final guest output, but do not wait forever on a worker
			// that retains the console after the main process has exited.
			con.conn.SetReadDeadline(time.Now().Add(shutdownGrace))

			select {
			case <-ctx.Done():
				con.Close()
			case <-stopWatch:
			}
		case <-stopWatch:
		}
	}()

	_, outputErr := io.Copy(os.Stdout, con.Reader())

	stopInput()
	con.Close()

	close(stopWatch)
	<-watchDone

	inputErr := <-inputDone

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if errors.Is(inputErr, context.Canceled) {
		inputErr = nil
	}

	if errors.Is(outputErr, os.ErrDeadlineExceeded) {
		select {
		case <-exited:
			outputErr = nil
		default:
		}
	}

	return errors.Join(relayError(inputErr), relayError(outputErr))
}

func relayError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) || consoleDisconnected(err) {
		return nil
	}

	return err
}

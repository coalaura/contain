package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

type terminalSize struct {
	columns int
	rows    int
}

type terminalResize struct {
	control      *console
	cancel       context.CancelFunc
	events       chan struct{}
	stopWatching func()
	done         chan struct{}
	err          error
}

func (resize *terminalResize) Close() error {
	resize.cancel()
	resize.control.Close()
	<-resize.done

	return resize.err
}

func (resize *terminalResize) notify() {
	if resize == nil {
		return
	}

	select {
	case resize.events <- struct{}{}:
	default:
	}
}

func (resize *terminalResize) monitor(ctx context.Context, descriptor int, size terminalSize) {
	defer close(resize.done)
	defer resize.control.Close()
	defer resize.stopWatching()

	for {
		select {
		case <-ctx.Done():
			return
		case <-resize.events:
		}

		current, err := readTerminalSize(descriptor)
		if err != nil || current == size {
			continue
		}

		err = sendTerminalSize(ctx, resize.control, current)
		if err != nil {
			if ctx.Err() == nil {
				resize.err = err
			}

			return
		}

		size = current
	}
}

func startTerminalResize(ctx context.Context, con *console, address string, descriptor int) (*terminalResize, error) {
	control, err := ConnectConsole(ctx, address, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("connect terminal resize channel: %w", err)
	}

	err = con.runShell(ctx, guestResizeCommand)
	if err == nil {
		// Wait until the guest has opened the UART so the initial update cannot be lost.
		err = waitTerminalResize(ctx, control)
	}

	if err != nil {
		control.Close()

		return nil, fmt.Errorf("initialize guest terminal resizing: %w", err)
	}

	resizeCtx, cancel := context.WithCancel(ctx)

	resize := &terminalResize{
		control: control,
		cancel:  cancel,
		events:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}

	// Subscribe before taking the initial snapshot so a startup resize is not lost.
	resize.stopWatching = watchTerminalResize(resize)

	size, err := readTerminalSize(descriptor)
	if err == nil {
		err = sendTerminalSize(ctx, control, size)
	}

	if err != nil {
		cancel()

		resize.stopWatching()
		control.Close()

		return nil, fmt.Errorf("initialize guest terminal size: %w", err)
	}

	go resize.monitor(resizeCtx, descriptor, size)

	return resize, nil
}

func terminalDescriptor() int {
	descriptor := int(os.Stdout.Fd())
	if term.IsTerminal(descriptor) {
		return descriptor
	}

	descriptor = int(os.Stdin.Fd())
	if term.IsTerminal(descriptor) {
		return descriptor
	}

	return -1
}

func readTerminalSize(descriptor int) (terminalSize, error) {
	columns, rows, err := term.GetSize(descriptor)
	if err != nil {
		return terminalSize{}, err
	}

	if columns <= 0 || rows <= 0 || columns > 65535 || rows > 65535 {
		return terminalSize{}, errors.New("terminal dimensions are outside 1..65535")
	}

	return terminalSize{columns: columns, rows: rows}, nil
}

func sendTerminalSize(ctx context.Context, control *console, size terminalSize) error {
	var frame [4]byte

	binary.LittleEndian.PutUint16(frame[0:2], uint16(size.columns))
	binary.LittleEndian.PutUint16(frame[2:4], uint16(size.rows))

	return exchangeTerminalResize(ctx, control, frame[:])
}

func waitTerminalResize(ctx context.Context, control *console) error {
	return exchangeTerminalResize(ctx, control, nil)
}

func exchangeTerminalResize(ctx context.Context, control *console, frame []byte) error {
	deadline := time.Now().Add(5 * time.Second)
	contextDeadline, ok := ctx.Deadline()

	if ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}

	err := control.conn.SetReadDeadline(deadline)
	if err != nil {
		return err
	}

	err = control.conn.SetWriteDeadline(deadline)
	if err != nil {
		return err
	}

	canceled := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		control.conn.SetReadDeadline(time.Now())
		control.conn.SetWriteDeadline(time.Now())

		close(canceled)
	})

	defer func() {
		if !stop() {
			<-canceled
		}

		control.conn.SetReadDeadline(time.Time{})
		control.conn.SetWriteDeadline(time.Time{})
	}()

	if len(frame) != 0 {
		_, err = control.conn.Write(frame)
		if err != nil {
			return fmt.Errorf("send guest terminal size: %w", err)
		}
	}

	// The helper sends zero when ready and after each successful resize.
	status, err := control.reader.ReadByte()
	if err != nil {
		return fmt.Errorf("wait for guest terminal resize: %w", err)
	}

	if status != 0 {
		return fmt.Errorf("guest terminal resize returned status %d", status)
	}

	return nil
}

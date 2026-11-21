package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	terminalResizeInterval = 250 * time.Millisecond
	terminalResizeMarker   = "__CONTAIN_RESIZE:"
)

type terminalSize struct {
	columns int
	rows    int
}

type terminalResize struct {
	control *console
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

//go:embed resize_guest.sh
var guestResizeScript string

var guestResizeCommand = "stty -F /dev/ttyS1 115200 raw -echo clocal -crtscts && setsid -f bash -c " + shellQuote(strings.ReplaceAll(strings.ReplaceAll(guestResizeScript, "\r\n", "\n"), "\t", "")) + " contain /dev/ttyS1 /dev/hvc0 </dev/null >/dev/null 2>&1"

func (resize *terminalResize) Close() error {
	resize.cancel()
	resize.control.Close()
	<-resize.done

	return resize.err
}

func (resize *terminalResize) monitor(ctx context.Context, descriptor int, size terminalSize) {
	defer close(resize.done)
	defer resize.control.Close()

	// Polling also covers Windows console resizing without consuming input events.
	ticker := time.NewTicker(terminalResizeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
	size, err := readTerminalSize(descriptor)
	if err != nil {
		return nil, fmt.Errorf("read host terminal size: %w", err)
	}

	control, err := ConnectConsole(ctx, address, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("connect terminal resize channel: %w", err)
	}

	err = con.runShell(ctx, guestResizeCommand)
	if err == nil {
		// Wait until the guest has opened the UART so the initial update cannot be lost.
		err = waitTerminalResize(ctx, control)
	}

	if err == nil {
		err = sendTerminalSize(ctx, control, size)
	}

	if err != nil {
		control.Close()

		return nil, fmt.Errorf("initialize guest terminal resizing: %w", err)
	}

	resizeCtx, cancel := context.WithCancel(ctx)

	resize := &terminalResize{
		control: control,
		cancel:  cancel,
		done:    make(chan struct{}),
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
	err := control.sendLine(ctx, fmt.Sprintf("%d %d", size.rows, size.columns))
	if err != nil {
		return fmt.Errorf("send guest terminal size: %w", err)
	}

	return waitTerminalResize(ctx, control)
}

func waitTerminalResize(ctx context.Context, control *console) error {
	status, _, err := control.waitForStatus(ctx, 5*time.Second, terminalResizeMarker)
	if err != nil {
		return fmt.Errorf("wait for guest terminal resize: %w", err)
	}

	if status != 0 {
		return fmt.Errorf("guest terminal resize returned exit status %d", status)
	}

	return nil
}

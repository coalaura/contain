package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/coalaura/contain/internal/mount"
)

const (
	signalNone consoleSignal = iota
	signalLogin
	signalPassword
	signalShell
	signalRejected
)

type console struct {
	conn   consoleConn
	reader *bufio.Reader
}

type consoleConn interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type consoleSignal uint8

var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func (c *console) Close() error {
	return c.conn.Close()
}

func (c *console) Reader() io.Reader {
	return c.reader
}

func (c *console) Writer() io.Writer {
	return c.conn
}

func (c *console) waitSignal(ctx context.Context, timeout time.Duration, detect func(*consolePrompt) consoleSignal) (consoleSignal, error) {
	var prompt consolePrompt

	event, _, err := c.readUntil(ctx, timeout, func(character byte) (bool, consoleSignal) {
		if !prompt.push(character) {
			return false, signalNone
		}

		value := detect(&prompt)

		return value != signalNone, value
	})

	return event, err
}

func (c *console) readUntil(ctx context.Context, timeout time.Duration, match func(byte) (bool, consoleSignal)) (consoleSignal, []byte, error) {
	deadline := time.Now().Add(timeout)

	d, ok := ctx.Deadline()
	if ok && d.Before(deadline) {
		deadline = d
	}

	err := c.conn.SetReadDeadline(deadline)
	if err != nil {
		return signalNone, nil, err
	}

	canceled := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		c.conn.SetReadDeadline(time.Now())

		close(canceled)
	})

	defer func() {
		if !stop() {
			<-canceled
		}

		c.conn.SetReadDeadline(time.Time{})
	}()

	tail := make([]byte, 0, 8192)

	for {
		b, err := c.reader.ReadByte()
		if err != nil {
			return signalNone, tail, err
		}

		if len(tail) == cap(tail) {
			copy(tail, tail[4096:])

			tail = tail[:4096]
		}

		tail = append(tail, b)

		ok, event := match(b)
		if ok {
			return event, tail, nil
		}
	}
}

func (c *console) sendLine(ctx context.Context, text string) error {
	return c.send(ctx, text+"\n")
}

func (c *console) send(ctx context.Context, text string) error {
	deadline := time.Now().Add(5 * time.Second)

	d, ok := ctx.Deadline()
	if ok && d.Before(deadline) {
		deadline = d
	}

	err := c.conn.SetWriteDeadline(deadline)
	if err != nil {
		return err
	}

	canceled := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		c.conn.SetWriteDeadline(time.Now())

		close(canceled)
	})

	defer func() {
		if !stop() {
			<-canceled
		}

		c.conn.SetWriteDeadline(time.Time{})
	}()

	_, err = io.WriteString(c.conn, text)
	return err
}

func ConnectConsole(ctx context.Context, address string, output io.Writer) (*console, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var lastErr error

	for {
		conn, err := dialConsole(ctx, address)
		if err == nil {
			// Log before buffering so startup and read-ahead bytes are recorded exactly once.
			return &console{
				conn:   conn,
				reader: bufio.NewReader(io.TeeReader(conn, output)),
			}, nil
		}

		lastErr = err

		if ctx.Err() != nil {
			return nil, fmt.Errorf("connect to guest console %s: %w (last dial: %v)", address, ctx.Err(), lastErr)
		}

		if !consoleNotReady(err) {
			return nil, fmt.Errorf("connect to guest console %s: %w", address, err)
		}

		timer := time.NewTimer(100 * time.Millisecond)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, fmt.Errorf("connect to guest console %s: %w (last dial: %v)", address, ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func InitializeGuest(ctx context.Context, con *console, login ConfigLogin, mounts []mount.Mount) error {
	if login.Username == "" {
		login.Username = "root"
	}

	if strings.ContainsAny(login.Username, "\r\n\x00") || login.Username == "" {
		return errors.New("invalid guest username")
	}

	err := mount.Validate(mounts)
	if err != nil {
		return err
	}

	_, err = con.waitSignal(ctx, 90*time.Second, func(prompt *consolePrompt) consoleSignal {
		if prompt.login() {
			return signalLogin
		}

		return signalNone
	})

	if err != nil {
		return fmt.Errorf("wait for guest login prompt (is getty configured on /dev/hvc0?): %w", err)
	}

	err = con.sendLine(ctx, login.Username)
	if err != nil {
		return fmt.Errorf("send guest username: %w", err)
	}

	state, err := con.waitSignal(ctx, 20*time.Second, func(prompt *consolePrompt) consoleSignal {
		switch {
		case prompt.rejected():
			return signalRejected
		case prompt.password():
			return signalPassword
		case prompt.shell():
			return signalShell
		}

		return signalNone
	})

	if err != nil {
		return fmt.Errorf("wait for guest authentication response: %w", err)
	}

	if state == signalRejected {
		return errors.New("guest rejected username")
	}

	if state == signalPassword {
		if login.Password == "" {
			return errors.New("guest requires a password but no password was configured")
		}

		if strings.ContainsAny(login.Password, "\r\n\x00") {
			return errors.New("guest password cannot contain CR, LF or NUL for console login")
		}

		err = con.sendLine(ctx, login.Password)
		if err != nil {
			return fmt.Errorf("send guest password: %w", err)
		}

		state, err = con.waitSignal(ctx, 20*time.Second, func(prompt *consolePrompt) consoleSignal {
			switch {
			case prompt.rejected(), prompt.login():
				return signalRejected
			case prompt.shell():
				return signalShell
			}

			return signalNone
		})

		if err != nil {
			return fmt.Errorf("wait for shell after password authentication: %w", err)
		}

		if state == signalRejected {
			return errors.New("guest rejected credentials")
		}
	}

	err = con.suspendHistory(ctx)
	if err != nil {
		return fmt.Errorf("suspend guest Bash history: %w", err)
	}

	err = con.runShell(ctx, `[[ $EUID == 0 ]]`)
	if err != nil {
		return fmt.Errorf("guest login must have root privileges: %w", err)
	}

	// The mount watcher outlives setup, so run its script from /run rather than the export.
	err = con.runShell(ctx, guestHelperCommand)
	if err != nil {
		return fmt.Errorf("prepare guest helper export: %w", err)
	}

	err = con.runShell(ctx, guestShutdownSetupCommand)
	if err != nil {
		return fmt.Errorf("prepare guest shutdown: %w", err)
	}

	directory := "/"

	if len(mounts) > 0 {
		err = mountGuestFilesystems(ctx, con, mounts)
		if err != nil {
			return err
		}

		directory = mounts[0].GuestPath
	}

	err = con.runShell(ctx, "cd "+shellQuote(directory))
	if err != nil {
		return fmt.Errorf("change guest directory to %s: %w", directory, err)
	}

	return nil
}

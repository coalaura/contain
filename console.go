package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
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

var (
	loginPrompt    = regexp.MustCompile(`(?i)(?:^|[\r\n])[^\r\n]{0,160}login:[ \t]*$`)
	passwordPrompt = regexp.MustCompile(`(?i)(?:^|[\r\n])[^\r\n]{0,160}password:[ \t]*$`)
	shellPrompt    = regexp.MustCompile(`(?:^|[\r\n])[^\r\n]{0,160}[#$][ \t]*$`)
	loginRejected  = regexp.MustCompile(`(?i)login incorrect|authentication failure|login failed`)
	ansiCSI        = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

func (c *console) Close() error {
	return c.conn.Close()
}

func (c *console) Reader() io.Reader {
	return c.reader
}

func (c *console) Writer() io.Writer {
	return c.conn
}

func (c *console) waitSignal(ctx context.Context, timeout time.Duration, detect func([]byte) consoleSignal) (consoleSignal, error) {
	event, _, err := c.readUntil(ctx, timeout, func(tail []byte) (bool, consoleSignal) {
		value := detect(tail)

		return value != signalNone, value
	})

	return event, err
}

func (c *console) readUntil(ctx context.Context, timeout time.Duration, match func([]byte) (bool, consoleSignal)) (consoleSignal, []byte, error) {
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

	tail := make([]byte, 0, 4096)

	for {
		b, err := c.reader.ReadByte()
		if err != nil {
			return signalNone, tail, err
		}

		tail = append(tail, b)
		if len(tail) > 8192 {
			copy(tail, tail[4096:])

			tail = tail[:4096]
		}

		ok, event := match(tail)
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

func (c *console) runShell(ctx context.Context, script string) error {
	var id [16]byte

	_, err := rand.Read(id[:])
	if err != nil {
		return err
	}

	token := hex.EncodeToString(id[:])
	left := "__CONTAIN_" + token[:16]
	right := token[16:]
	beginMarker := left + right + "_BEGIN"
	marker := left + right + "_DONE:"

	// Split both markers so echoed input cannot be mistaken for command output.
	prefix := "printf '%s%s_BEGIN\\n' '" + left + "' '" + right + "'; "
	payload := prefix + script + "; __contain_status=$?; printf '%s%s_DONE:%d\\n' '" + left + "' '" + right + "' \"$__contain_status\"\n"

	err = c.send(ctx, payload)
	if err != nil {
		return err
	}

	status, tail, err := c.waitForStatus(ctx, 30*time.Second, marker)

	_, output, found := bytes.Cut(tail, []byte(beginMarker))
	if found {
		tail = output
	}

	tail, _, _ = bytes.Cut(tail, []byte(marker))

	if err != nil {
		return fmt.Errorf("guest shell command timed out or failed to respond: %w (console: %s)", err, consoleExcerpt(tail))
	}

	if status != 0 {
		return fmt.Errorf("guest shell returned exit status %d (console: %s)", status, consoleExcerpt(tail))
	}

	return nil
}

func (c *console) waitForStatus(ctx context.Context, timeout time.Duration, marker string) (int, []byte, error) {
	status := -1

	_, tail, err := c.readUntil(ctx, timeout, func(tail []byte) (bool, consoleSignal) {
		idx := bytes.LastIndex(tail, []byte(marker))
		if idx < 0 {
			return false, signalNone
		}

		after := tail[idx+len(marker):]

		line, _, found := bytes.Cut(after, []byte{'\n'})
		if !found {
			return false, signalNone
		}

		n, parseErr := strconv.Atoi(strings.TrimSpace(string(line)))
		if parseErr != nil {
			return false, signalNone
		}

		status = n

		return true, signalShell
	})

	return status, tail, err
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

func InitializeGuest(ctx context.Context, con *console, login ConfigLogin, mounts []GuestMount) error {
	if login.Username == "" {
		login.Username = "root"
	}

	if strings.ContainsAny(login.Username, "\r\n\x00") || login.Username == "" {
		return errors.New("invalid guest username")
	}

	err := ValidateGuestMounts(mounts)
	if err != nil {
		return err
	}

	_, err = con.waitSignal(ctx, 90*time.Second, func(tail []byte) consoleSignal {
		if loginPrompt.Match(ansiCSI.ReplaceAll(tail, nil)) {
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

	state, err := con.waitSignal(ctx, 20*time.Second, func(tail []byte) consoleSignal {
		clean := ansiCSI.ReplaceAll(tail, nil)

		switch {
		case loginRejected.Match(clean):
			return signalRejected
		case passwordPrompt.Match(clean):
			return signalPassword
		case shellPrompt.Match(clean):
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

		state, err = con.waitSignal(ctx, 20*time.Second, func(tail []byte) consoleSignal {
			clean := ansiCSI.ReplaceAll(tail, nil)

			switch {
			case loginRejected.Match(clean), loginPrompt.Match(clean):
				return signalRejected
			case shellPrompt.Match(clean):
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

	err = con.runShell(ctx, `test "$(id -u)" -eq 0`)
	if err != nil {
		return fmt.Errorf("guest login must have root privileges: %w", err)
	}

	err = mountGuestFilesystems(ctx, con, mounts)
	if err != nil {
		return err
	}

	err = con.runShell(ctx, "cd /workspace")
	if err != nil {
		return fmt.Errorf("change guest directory to /workspace: %w", err)
	}

	return nil
}

func consoleExcerpt(tail []byte) string {
	clean := strings.ReplaceAll(string(ansiCSI.ReplaceAll(tail, nil)), "\r", "")
	clean = strings.TrimSpace(clean)

	if len(clean) > 700 {
		clean = clean[len(clean)-700:]
	}

	return clean
}

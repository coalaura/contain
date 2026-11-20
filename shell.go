package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

//go:embed command_guest.sh
var guestCommandScript string

// Tabs would trigger completion in the interactive console while transmitting the script.
var guestCommandPrefix = "sh -c " + shellQuote(strings.ReplaceAll(guestCommandScript, "\t", "")) + " contain "

func (c *console) startCommand(ctx context.Context, command string) error {
	if command == "" {
		return nil
	}

	token := rand.Text()
	left := "__CONTAIN_" + token[:16]
	right := token[16:]
	marker := left + right + "_READY:"

	// Split the marker so echoed input cannot be mistaken for supervisor output.
	payload := guestCommandPrefix + shellQuote(command) + " " + shellQuote(left) + " " + shellQuote(right)

	err := c.sendLine(ctx, payload)
	if err != nil {
		return fmt.Errorf("start guest command: %w", err)
	}

	// Consume the echoed supervisor input, but never wait for the interactive command to exit.
	_, tail, err := c.waitForStatus(ctx, 30*time.Second, marker)
	if err != nil {
		return fmt.Errorf("wait for guest command startup: %w (console: %s)", err, consoleExcerpt(tail))
	}

	return nil
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

func validateShellCommand(command string) error {
	for _, character := range command {
		if character < ' ' || character == '\x7f' {
			return errors.New("must be a single-line shell command without control characters")
		}
	}

	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func consoleExcerpt(tail []byte) string {
	clean := strings.ReplaceAll(string(ansiCSI.ReplaceAll(tail, nil)), "\r", "")
	clean = strings.TrimSpace(clean)

	if len(clean) > 700 {
		clean = clean[len(clean)-700:]
	}

	return clean
}

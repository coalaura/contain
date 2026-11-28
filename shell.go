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

const (
	guestSuspendHistoryCommand = `__contain_history_enabled=0; if [[ -o history ]]; then __contain_history_enabled=1; fi; set +o history; case "$(HISTTIMEFORMAT= builtin history 1)" in *@TOKEN@*) builtin history -d -1 ;; esac`
	guestResumeHistoryCommand  = `if [[ $__contain_history_enabled == 1 ]]; then set -o history; fi; unset __contain_history_enabled`
	guestShutdownSetupCommand  = "bash /run/contain-guest/shutdown.sh"
)

//go:embed shutdown_guest.sh
var guestShutdownScript string

//go:embed shutdown_setup_guest.sh
var guestShutdownSetupScript []byte

//go:embed command_guest.sh
var guestCommandScript string

// Tabs would trigger completion in the interactive console while transmitting the script.
var (
	guestShutdownCommand = strings.ReplaceAll(guestShutdownScript, "\t", "")
	guestCommandPrefix   = "exec bash -c " + shellQuote(strings.ReplaceAll(guestShutdownScript+"\n"+guestCommandScript, "\t", "")) + " contain "
)

func (c *console) suspendHistory(ctx context.Context) error {
	// Keep the bootstrap on one input line, even when Bash's cmdhist option is off.
	// Its unique token prevents deleting user history if Bash ignored this command.
	script := strings.ReplaceAll(guestSuspendHistoryCommand, "@TOKEN@", rand.Text())

	return c.runShell(ctx, script)
}

func (c *console) startCommand(ctx context.Context, command string) error {
	token := rand.Text()
	left := "__CONTAIN_" + token[:16]
	right := token[16:]
	marker := left + right + "_READY:"
	c.exitMarker = "\x1e" + left + right + "_EXIT:"

	if command == "" {
		script := strings.ReplaceAll(guestShutdownCommand, "@LEFT@", left)
		script = strings.ReplaceAll(script, "@RIGHT@", right)

		return c.runShell(ctx, "eval "+shellQuote(script)+" && "+guestResumeHistoryCommand)
	}

	// Restore history in the same input that starts the command, before it takes over stdin.
	// Split the marker to distinguish output from echoed input.
	// Replace the login shell so it cannot keep the workspace busy during shutdown.
	prefix := strings.ReplaceAll(guestCommandPrefix, "@LEFT@", left)
	prefix = strings.ReplaceAll(prefix, "@RIGHT@", right)
	payload := guestResumeHistoryCommand + "; " + prefix + shellQuote(command) + " " + shellQuote(left) + " " + shellQuote(right)

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
	line := make([]byte, 0, 8192)

	_, tail, err := c.readUntil(ctx, timeout, func(character byte) (bool, consoleSignal) {
		if character != '\n' {
			if len(line) == cap(line) {
				copy(line, line[4096:])

				line = line[:4096]
			}

			line = append(line, character)

			return false, signalNone
		}

		complete := line
		line = line[:0]

		idx := bytes.LastIndex(complete, []byte(marker))
		if idx < 0 {
			return false, signalNone
		}

		n, parseErr := strconv.Atoi(strings.TrimSpace(string(complete[idx+len(marker):])))
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

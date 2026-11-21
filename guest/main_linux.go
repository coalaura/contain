package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func main() {
	arguments := os.Args[1:]
	serving := len(arguments) > 0 && arguments[0] == "--serve"

	if serving {
		arguments = arguments[1:]
	}

	controlPath := "/dev/ttyS1"
	terminalPath := "/dev/hvc0"

	if len(arguments) == 2 {
		controlPath = arguments[0]
		terminalPath = arguments[1]
	} else if len(arguments) != 0 {
		fmt.Fprintln(os.Stderr, "usage: resize [control-device terminal-device]")

		os.Exit(1)
	}

	var err error

	if serving {
		ready := os.NewFile(3, "ready")

		err = serveResize(controlPath, terminalPath, ready)
		if err != nil {
			fmt.Fprintln(ready, err)
		}

		ready.Close()
	} else {
		err = startResize(controlPath, terminalPath)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func startResize(controlPath, terminalPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}

	defer reader.Close()
	defer writer.Close()

	// Re-exec in a new session with no console stdio. The pipe reports startup
	// failures before the parent returns control to the guest login shell.
	command := exec.Command(executable, "--serve", controlPath, terminalPath)

	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.ExtraFiles = []*os.File{writer}

	err = command.Start()
	if err != nil {
		return err
	}

	writer.Close()

	message, err := bufio.NewReader(reader).ReadString('\n')
	if err == nil && message == "ready\n" {
		return command.Process.Release()
	}

	command.Process.Kill()
	command.Wait()

	if message != "" {
		return fmt.Errorf("start resize listener: %s", strings.TrimSpace(message))
	}

	return fmt.Errorf("start resize listener: %w", err)
}

func serveResize(controlPath, terminalPath string, ready *os.File) error {
	control, err := os.OpenFile(controlPath, os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open resize channel: %w", err)
	}

	defer control.Close()

	err = configureSerial(int(control.Fd()))
	if err != nil {
		return fmt.Errorf("configure resize channel: %w", err)
	}

	terminal, err := os.OpenFile(terminalPath, os.O_WRONLY|unix.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open guest terminal: %w", err)
	}

	defer terminal.Close()

	var reply [1]byte

	_, err = control.Write(reply[:])
	if err != nil {
		return err
	}

	_, err = io.WriteString(ready, "ready\n")
	ready.Close()

	if err != nil {
		return err
	}

	var frame [4]byte

	for {
		_, readErr := io.ReadFull(control, frame[:])

		if errors.Is(readErr, io.EOF) {
			return nil
		}

		if readErr != nil {
			return readErr
		}

		// Each frame is uint16 columns, uint16 rows, both little-endian.
		size := unix.Winsize{
			Col: binary.LittleEndian.Uint16(frame[0:2]),
			Row: binary.LittleEndian.Uint16(frame[2:4]),
		}
		reply[0] = 1

		if size.Col != 0 && size.Row != 0 {
			err = unix.IoctlSetWinsize(int(terminal.Fd()), unix.TIOCSWINSZ, &size)
			if err == nil {
				reply[0] = 0
			}
		}

		_, err = control.Write(reply[:])
		if err != nil {
			return err
		}
	}
}

func configureSerial(descriptor int) error {
	settings, err := unix.IoctlGetTermios(descriptor, unix.TCGETS)
	if err != nil {
		return err
	}

	settings.Iflag = 0
	settings.Oflag = 0
	settings.Lflag = 0
	settings.Cflag = unix.B115200 | unix.CS8 | unix.CREAD | unix.CLOCAL
	settings.Ispeed = unix.B115200
	settings.Ospeed = unix.B115200
	settings.Cc[unix.VMIN] = 1
	settings.Cc[unix.VTIME] = 0

	return unix.IoctlSetTermios(descriptor, unix.TCSETS, settings)
}

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const installTimeout = 20 * time.Minute

func installAlpine(ctx context.Context, machine *MachineConfig, modloop, marker string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	address, cleanup, err := consoleAddress()
	if err != nil {
		return err
	}

	defer cleanup()

	cmdline := "console=hvc0 rdinit=/contain-install.sh"

	args, err := machineArgs(machine, address, cmdline, false, 12)
	if err != nil {
		return err
	}

	exportDirectory := filepath.Join(filepath.Dir(machine.disk), "boot")

	err = os.Mkdir(exportDirectory, 0700)
	if err != nil {
		return err
	}

	args = append(args, "--pcie-root-port", "rc0:export", "--virtio-fs", "pcie_port=export:contain_export,"+exportDirectory)
	args = append(args, "--pcie-root-port", "rc0:net", "--virtio-net", "pcie_port=net:consomme")
	args = append(args, "--pcie-root-port", "rc0:modloop", "--virtio-blk", "file:"+modloop+",ro,pcie_port=modloop,serial=contain-modloop")

	processCtx, stopProcess := context.WithCancel(ctx)
	defer stopProcess()

	connectCtx, stopConnect := context.WithCancel(ctx)
	defer stopConnect()

	cmd := exec.CommandContext(processCtx, paths.OpenVMM, args...)

	cmd.Stdout = output
	cmd.Stderr = output

	cmd.WaitDelay = shutdownGrace

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create OpenVMM stdin pipe: %w", err)
	}

	defer stdinReader.Close()
	defer stdinWriter.Close()

	cmd.Stdin = stdinReader

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("start OpenVMM installer: %w", err)
	}

	stdinReader.Close()

	exited := make(chan struct{})

	var processErr error

	go func() {
		processErr = cmd.Wait()

		close(exited)
		stopConnect()
	}()

	defer func() {
		stopProcess()
		<-exited
	}()

	con, err := ConnectConsole(connectCtx, address, output)
	if err != nil {
		return startupError(ctx, exited, &processErr, err)
	}

	defer con.Close()

	// Keep reading buffered console output after OpenVMM exits, so a fast
	// shutdown cannot hide the installer's final status marker.
	status, tail, err := con.waitForStatus(ctx, installTimeout, marker)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return fmt.Errorf("wait for Alpine installer: %w (console: %s)", err, consoleExcerpt(tail))
	}

	if status != 0 {
		return fmt.Errorf("installer exited with status %d (console: %s)", status, consoleExcerpt(tail))
	}

	drained := make(chan struct{})

	go func() {
		io.Copy(io.Discard, con.Reader())
		close(drained)
	}()

	defer func() {
		con.Close()
		<-drained
	}()

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case <-exited:
		if processErr != nil {
			return fmt.Errorf("OpenVMM installer shutdown: %w", processErr)
		}

		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("installer did not shut down within %s", 30*time.Second)
	}
}

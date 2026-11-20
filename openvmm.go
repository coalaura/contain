package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const shutdownGrace = 2 * time.Second

func RunOpenVMM(ctx context.Context) error {
	log.Stepln("Starting VM...")

	output, err := os.CreateTemp(paths.DataDirectory, "contain-*.log")
	if err != nil {
		return fmt.Errorf("create session log: %w", err)
	}

	log.Subf("Session log: %s\n", output.Name())

	err = runOpenVMM(ctx, output)
	if err != nil {
		fmt.Fprintln(output, err)
	}

	closeErr := output.Close()

	err = errors.Join(err, closeErr)
	if err != nil {
		return fmt.Errorf("%w (log: %s)", err, output.Name())
	}

	return nil
}

func runOpenVMM(ctx context.Context, output *os.File) error {
	address, cleanup, err := consoleAddress()
	if err != nil {
		return err
	}

	defer cleanup()

	args, err := openVMMArgs(address)
	if err != nil {
		return err
	}

	processCtx, stopProcess := context.WithCancel(ctx)
	defer stopProcess()

	consoleCtx, stopConsole := context.WithCancel(ctx)
	defer stopConsole()

	cmd := exec.CommandContext(processCtx, paths.OpenVMM, args...)

	cmd.Stdout = output
	cmd.Stderr = output

	cmd.WaitDelay = shutdownGrace

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create OpenVMM stdin pipe: %w", err)
	}

	defer stdinWriter.Close()
	defer stdinReader.Close()

	cmd.Stdin = stdinReader

	start := time.Now()

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("start OpenVMM: %w", err)
	}

	stdinReader.Close()

	exited := make(chan struct{})

	var processErr error

	go func() {
		processErr = cmd.Wait()

		close(exited)
		stopConsole()
	}()

	defer func() {
		stopProcess()
		<-exited
	}()

	log.Subln("Connecting to guest console...")

	con, err := ConnectConsole(consoleCtx, address, output)
	if err != nil {
		return startupError(ctx, exited, &processErr, err)
	}

	defer con.Close()

	mounts := []GuestMount{
		{
			Tag:       "workspace",
			GuestPath: "/workspace",
		},
	}

	log.Subln("Initializing guest...")

	err = initializeGuest(consoleCtx, con, config.Machine.Login, mounts)
	if err != nil {
		return startupError(ctx, exited, &processErr, err)
	}

	took := time.Since(start)

	log.Successf("Guest ready in %s.\n", took)

	relayErr := relayTerminal(ctx, con, exited)
	con.Close()

	// A console can disconnect without OpenVMM exiting. Give poweroff time to
	// finish, then let deferred cancellation terminate and reap the process.
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()

	select {
	case <-exited:
		return errors.Join(relayErr, processErr)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return relayErr
	}
}

func openVMMArgs(address string) ([]string, error) {
	// exec.Command quotes OS arguments. OpenVMM's compound values have no
	// escaping syntax, so reject delimiters that would change their meaning.
	if strings.ContainsAny(config.Source.disk, ",;\x00") || !balancedBrackets(config.Source.disk) {
		return nil, fmt.Errorf("OpenVMM cannot represent disk path %q: commas, semicolons, NUL and unbalanced brackets are unsupported", config.Source.disk)
	}

	if strings.ContainsAny(paths.WorkingDirectory, ",\x00") {
		return nil, fmt.Errorf("OpenVMM cannot represent workspace path %q: commas and NUL are unsupported", paths.WorkingDirectory)
	}

	if strings.ContainsAny(address, ",=\x00") {
		return nil, fmt.Errorf("OpenVMM cannot represent console path %q: commas, equals signs and NUL are unsupported", address)
	}

	return []string{
		"--hv",
		"--processors", strconv.Itoa(config.Machine.Processors),
		"--memory", fmt.Sprintf("%dG", config.Machine.Memory),
		"--kernel", config.Source.kernel,
		"--initrd", config.Source.initrd,
		"--virtio-blk", "file:" + config.Source.disk + ",pcie_port=disk",
		"--cmdline", "root=/dev/vda rootfstype=ext4 rootflags=rw console=hvc0 modules=virtio_pci,virtio_blk,ext4 contain.mounts=1",
		"--pcie-root-complex", "rc0",
		"--pcie-root-port", "rc0:disk",
		"--pcie-root-port", "rc0:console",
		"--com1", "none",
		"--virtio-console", "listen=" + address,
		"--virtio-console-pcie-port", "console",
		"--pcie-root-port", "rc0:net",
		"--virtio-net", "pcie_port=net:consomme",
		"--pcie-root-port", "rc0:fs0",
		"--virtio-fs", "pcie_port=fs0:workspace," + paths.WorkingDirectory,
		"--pcie-root-port", "rc0:fs1",
		// "--virtio-fs", "pcie_port=fs1:extra0,/home/user/datasets", // TODO: mount additional r/rw paths & escape
		"--guest-shutdown-action", "exit",
	}, nil
}

func startupError(ctx context.Context, exited <-chan struct{}, processErr *error, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	select {
	case <-exited:
		if *processErr != nil {
			return fmt.Errorf("OpenVMM exited before guest initialization: %w", *processErr)
		}

		return errors.New("OpenVMM exited before guest initialization")
	default:
		return err
	}
}

func balancedBrackets(value string) bool {
	depth := 0

	for _, char := range value {
		switch char {
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}

	return depth == 0
}

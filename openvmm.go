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

func RunOpenVMM(ctx context.Context, mounts []GuestMount, snapshots []GuestSnapshot, command string, temporary bool) error {
	log.Stepln("Starting VM...")

	output, err := os.CreateTemp(paths.DataDirectory, "contain-*.log")
	if err != nil {
		return fmt.Errorf("create session log: %w", err)
	}

	log.Subf("Session log: %s\n", output.Name())

	err = runOpenVMM(ctx, output, mounts, snapshots, command, temporary)
	if err != nil {
		fmt.Fprintln(output, err)
	}

	closeErr := output.Close()

	return errors.Join(err, closeErr)
}

func runOpenVMM(ctx context.Context, output *os.File, mounts []GuestMount, snapshots []GuestSnapshot, command string, temporary bool) error {
	address, cleanup, err := consoleAddress()
	if err != nil {
		return err
	}

	defer cleanup()

	descriptor := terminalDescriptor()

	var (
		resizeAddress   string
		helperDirectory string
	)

	if descriptor >= 0 {
		var cleanupResize func()

		resizeAddress, cleanupResize, err = consoleAddress()
		if err != nil {
			return err
		}

		defer cleanupResize()

		var cleanupHelper func()

		helperDirectory, cleanupHelper, err = prepareGuestHelper()
		if err != nil {
			return err
		}

		defer cleanupHelper()
	}

	snapshotDirectory, cleanupSnapshots, err := prepareGuestSnapshots(snapshots)
	if err != nil {
		return err
	}

	defer cleanupSnapshots()

	args, err := openVMMArgs(address, resizeAddress, helperDirectory, snapshotDirectory, mounts, temporary)
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

	log.Subln("Initializing guest...")

	err = InitializeGuest(consoleCtx, con, config.Machine.Login, mounts)
	if err != nil {
		return startupError(ctx, exited, &processErr, err)
	}

	err = copyGuestSnapshots(consoleCtx, con, snapshots)
	if err != nil {
		return startupError(ctx, exited, &processErr, err)
	}

	var resize *terminalResize

	if resizeAddress != "" {
		resize, err = startTerminalResize(consoleCtx, con, resizeAddress, descriptor)
		if err != nil {
			return startupError(ctx, exited, &processErr, err)
		}

		defer resize.Close()
	}

	err = con.startCommand(consoleCtx, command)
	if err != nil {
		return startupError(ctx, exited, &processErr, err)
	}

	took := time.Since(start).Round(time.Millisecond)

	log.Successf("Guest ready in %s.\n", took)

	relayErr := relayTerminal(ctx, con, exited, resize)
	con.Close()

	if resize != nil {
		relayErr = errors.Join(relayErr, resize.Close())
	}

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

func openVMMArgs(address, resizeAddress, helperDirectory, snapshotDirectory string, mounts []GuestMount, temporary bool) ([]string, error) {
	err := ValidateGuestMounts(mounts)
	if err != nil {
		return nil, err
	}

	cmdline := "root=/dev/vda rootfstype=ext4 rootflags=rw console=hvc0 modules=virtio_pci,virtio_blk,ext4"

	if len(mounts) > 0 {
		cmdline += " contain.mounts=1"
	}

	if strings.ContainsAny(resizeAddress, ",=\x00") {
		return nil, fmt.Errorf("OpenVMM cannot represent resize channel path %q: commas, equals signs and NUL are unsupported", resizeAddress)
	}

	if strings.ContainsAny(helperDirectory, ",\x00") {
		return nil, fmt.Errorf("OpenVMM cannot represent guest helper path %q: commas and NUL are unsupported", helperDirectory)
	}

	if strings.ContainsAny(snapshotDirectory, ",\x00") {
		return nil, fmt.Errorf("OpenVMM cannot represent snapshot directory %q: commas and NUL are unsupported", snapshotDirectory)
	}

	if (resizeAddress == "") != (helperDirectory == "") {
		return nil, errors.New("terminal resizing requires both a control channel and a guest helper")
	}

	args, err := machineArgs(&config.Machine, address, cmdline, temporary, 4*len(mounts)+10)
	if err != nil {
		return nil, err
	}

	if resizeAddress != "" {
		args = append(args, "--com2", "listen="+resizeAddress)
		args = append(args, "--pcie-root-port", "rc0:guest", "--virtio-fs", "pcie_port=guest:contain_guest,"+helperDirectory+",ro")
	}

	if snapshotDirectory != "" {
		args = append(args, "--pcie-root-port", "rc0:snapshots", "--virtio-fs", "pcie_port=snapshots:contain_snapshots,"+snapshotDirectory+",ro")
	}

	for index := range mounts {
		mount := &mounts[index]

		port := "fs" + strconv.Itoa(index)
		options := ""

		if mount.Readonly {
			options = ",ro"
		}

		args = append(args, "--pcie-root-port", "rc0:"+port, "--virtio-fs", "pcie_port="+port+":"+mount.Tag+","+mount.HostPath+options)
	}

	return args, nil
}

func machineArgs(machine *MachineConfig, address, cmdline string, temporary bool, extra int) ([]string, error) {
	// exec.Command quotes OS arguments. OpenVMM's compound values have no
	// escaping syntax, so reject delimiters that would change their meaning.
	if strings.ContainsAny(machine.disk, ",;\x00") || !balancedBrackets(machine.disk) {
		return nil, fmt.Errorf("OpenVMM cannot represent disk path %q: commas, semicolons, NUL and unbalanced brackets are unsupported", machine.disk)
	}

	if strings.ContainsAny(address, ",=\x00") {
		return nil, fmt.Errorf("OpenVMM cannot represent console path %q: commas, equals signs and NUL are unsupported", address)
	}

	disk := "file:" + machine.disk

	if temporary {
		disk = "memdiff:" + disk
	}

	baseArgs := []string{
		"--hv",
		"--processors", strconv.Itoa(machine.Processors),
		"--memory", fmt.Sprintf("%dG", machine.Memory),
		"--kernel", machine.kernel,
		"--initrd", machine.initrd,
		"--virtio-blk", disk + ",pcie_port=disk",
		"--cmdline", cmdline,
		"--pcie-root-complex", "rc0",
		"--pcie-root-port", "rc0:disk",
		"--pcie-root-port", "rc0:console",
		"--com1", "none",
		"--virtio-console", "listen=" + address,
		"--virtio-console-pcie-port", "console",
		"--pcie-root-port", "rc0:net",
		"--virtio-net", "pcie_port=net:consomme",
		"--guest-shutdown-action", "exit",
	}

	args := make([]string, 0, len(baseArgs)+extra)
	args = append(args, baseArgs...)

	return args, nil
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

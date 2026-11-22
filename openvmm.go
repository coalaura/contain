package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/coalaura/contain/internal/mount"
	"github.com/coalaura/contain/internal/network"
	"github.com/coalaura/contain/internal/sessionlog"
	"github.com/coalaura/contain/internal/snapshot"
	"github.com/coalaura/contain/internal/terminal"
)

const (
	shutdownGrace        = 2 * time.Second
	networkShutdownGrace = 10 * time.Second
)

func RunOpenVMM(ctx context.Context, mounts []mount.Mount, snapshots []snapshot.Snapshot, command string, temporary bool) error {
	log.Stepln("Starting VM...")

	output, err := sessionlog.New(paths.LogsDirectory, "contain-*.log", config.Logs.MaxBytes())
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

func runOpenVMM(ctx context.Context, output io.Writer, mounts []mount.Mount, snapshots []snapshot.Snapshot, command string, temporary bool) error {
	err := validateNetworkShares(mounts, snapshots)
	if err != nil {
		return err
	}

	authority, err := network.LoadAuthority(config.Machine.directory)
	if err != nil {
		return fmt.Errorf("load network CA: %w", err)
	}

	adapter := ""

	if config.Machine.Network.InspectHTTPS() {
		adapter = config.Machine.Network.Credentials
	}

	credentials, err := network.LoadCredentials(adapter, paths.DataDirectory)
	if err != nil {
		return err
	}

	gateway, err := network.Start(ctx, config.Machine.Network, config.Machine.DNS, authority, credentials)
	if err != nil {
		return err
	}

	defer gateway.Close()

	address, cleanup, err := consoleAddress()
	if err != nil {
		return err
	}

	defer cleanup()

	descriptor := terminal.Descriptor()

	helperDirectory, cleanupHelper, err := prepareGuestHelper(config.Machine.directory, authority.PublicCertificate())
	if err != nil {
		return err
	}

	defer cleanupHelper()

	var resizeAddress string

	if descriptor >= 0 {
		var cleanupResize func()

		resizeAddress, cleanupResize, err = consoleAddress()
		if err != nil {
			return err
		}

		defer cleanupResize()
	}

	snapshotDirectory, cleanupSnapshots, err := snapshot.Prepare(config.Machine.directory, snapshots)
	if err != nil {
		return err
	}

	defer cleanupSnapshots()

	args, err := openVMMArgs(address, resizeAddress, helperDirectory, snapshotDirectory, gateway.Path(), mounts, temporary)
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

	err = initializeGuestNetwork(consoleCtx, con)
	if err != nil {
		return err
	}

	err = gateway.WaitReady(consoleCtx)
	if err != nil {
		return err
	}

	// During startup, let the helper report setup errors before terminating the VM.
	// The gateway independently closes all host connections on transport failure.
	watchNetwork := make(chan struct{})

	var networkErr error

	go func() {
		defer close(watchNetwork)

		networkErr = waitNetworkShutdown(processCtx, gateway.Done(), exited)
		if networkErr != nil {
			stopConsole()
			stopProcess()
		}
	}()

	defer func() {
		stopProcess()
		<-watchNetwork
	}()

	if adapter == "opencode" {
		err = con.runShell(consoleCtx, "export OPENCODE_AUTH_CONTENT="+shellQuote(credentials.Placeholder()))
		if err != nil {
			return err
		}

		if credentials.Configuration() != "" {
			err = con.runShell(consoleCtx, "export OPENCODE_CONFIG_CONTENT="+shellQuote(credentials.Configuration()))
			if err != nil {
				return err
			}
		}
	}

	var resize *terminal.Resize

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
		<-watchNetwork

		return errors.Join(relayErr, networkErr, processErr)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return relayErr
	}
}

func waitNetworkShutdown(ctx context.Context, disconnected, exited <-chan struct{}) error {
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-disconnected:
	}

	// Poweroff stops the guest network helper before the VM exits. The gateway
	// has already closed host connections, so allow guest shutdown to finish.
	timer := time.NewTimer(networkShutdownGrace)
	defer timer.Stop()

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("guest network disconnected without VM shutdown")
	}
}

func openVMMArgs(address, resizeAddress, helperDirectory, snapshotDirectory, vsockPath string, mounts []mount.Mount, temporary bool) ([]string, error) {
	err := mount.Validate(mounts)
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

	if helperDirectory == "" || vsockPath == "" || strings.ContainsRune(vsockPath, '\x00') {
		return nil, errors.New("normal sessions require a guest helper and vsock gateway")
	}

	args, err := machineArgs(&config.Machine, address, cmdline, temporary, 4*len(mounts)+10)
	if err != nil {
		return nil, err
	}

	if resizeAddress != "" {
		args = append(args, "--com2", "listen="+resizeAddress)
	}

	args = append(args, "--pcie-root-port", "rc0:guest", "--virtio-fs", "pcie_port=guest:contain_guest,"+helperDirectory+",ro")
	args = append(args, "--pcie-root-port", "rc0:vsock", "--virtio-vsock-path", vsockPath, "--virtio-vsock-bus", "pcie:vsock")

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

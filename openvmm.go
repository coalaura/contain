package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/term"
)

func RunOpenVMM(ctx context.Context) error {
	cmd := exec.CommandContext(
		ctx,
		paths.OpenVMM,
		"--hv",
		"--processors", strconv.FormatInt(int64(config.Machine.Processors), 10),
		"--memory", fmt.Sprintf("%dG", config.Machine.Memory),
		"--kernel", config.Source.kernel, // TODO: escape
		"--initrd", config.Source.initrd, // TODO: escape
		"--virtio-blk", fmt.Sprintf("file:%s,pcie_port=disk", config.Source.disk), // TODO: escape
		"--cmdline", "root=/dev/vda rootfstype=ext4 rootflags=rw console=hvc0 modules=virtio_pci,virtio_blk,ext4 contain.mounts=1",
		"--pcie-root-complex", "rc0",
		"--pcie-root-port", "rc0:disk",
		"--pcie-root-port", "rc0:console",
		"--com1", "none",
		"--virtio-console", "listen=tcp:127.0.0.1:19385",
		"--virtio-console-pcie-port", "console",
		"--pcie-root-port", "rc0:net",
		"--virtio-net", "pcie_port=net:consomme",
		"--pcie-root-port", "rc0:fs0",
		"--virtio-fs", fmt.Sprintf("pcie_port=fs0:workspace,%s", paths.WorkingDirectory), // TODO: escape
		"--pcie-root-port", "rc0:fs1",
		// "--virtio-fs", "pcie_port=fs1:extra0,/home/user/datasets", // TODO: mount additional r/rw paths & escape
		"--guest-shutdown-action", "exit",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		return err
	}

	con, err := ConnectConsole(ctx, "127.0.0.1:19385")
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()

		return err
	}

	defer con.Close()

	mounts := []GuestMount{
		{
			Tag:       "workspace",
			GuestPath: "/workspace",
		},
	}

	err = initializeGuest(ctx, con, config.Machine.Login, mounts)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()

		return err
	}

	err = relayTerminal(con)
	if err != nil {
		cmd.Wait()

		return err
	}

	return cmd.Wait()
}

func relayTerminal(con *console) error {
	fd := int(os.Stdin.Fd())

	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}

		defer term.Restore(fd, state)
	}

	go func() {
		io.Copy(con.Writer(), os.Stdin)
	}()

	_, err := io.Copy(os.Stdout, con.Reader())
	return err
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func RunOpenVMM() error {
	cmd := exec.Command(
		paths.OpenVMM,
		"--hypervisor", "whp", // TODO: possibly determine dynamically based on OS
		"--hv",
		"--processors", strconv.FormatInt(int64(config.Machine.Processors), 10),
		"--memory", fmt.Sprintf("%dG", config.Machine.Memory),
		"--kernel", config.Source.kernel, // TODO: escape
		"--initrd", config.Source.initrd, // TODO: escape
		"--virtio-blk", fmt.Sprintf("file:%s,pcie_port=disk", config.Source.disk), // TODO: escape
		"--cmdline", `"root=/dev/vda rootfstype=ext4 rw console=hvc0 modules=virtio_pci,virtio_blk,ext4 contain.mounts=1"`,
		"--pcie-root-complex", "rc0",
		"--pcie-root-port", "rc0:disk",
		"--pcie-root-port", "rc0:console",
		"--com1", "none",
		"--virtio-console", "console",
		"--virtio-console-pcie-port", "console",
		"--pcie-root-port", "rc0:net",
		"--virtio-net", "pcie_port=net:consomme",
		"--pcie-root-port", "rc0:fs0",
		"--virtio-fs", fmt.Sprintf("pcie_port=fs0:workspace,%s", paths.WorkingDirectory), // TODO: escape
		"--pcie-root-port", "rc0:fs1",
		// "--virtio-fs", "pcie_port=fs1:extra0,/home/user/datasets", // TODO: mount additional r/rw paths & escape
		"--guest-shutdown-action", "exit",
	)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Start()
}

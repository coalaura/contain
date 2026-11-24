package create

import (
	"fmt"
	"io"
	"os"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

type netbootArtifact struct {
	name        string
	destination string
}

func ExtractNetboot(path, kernel, initrd, modloop string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}

	defer input.Close()

	info, err := input.Stat()
	if err != nil {
		return err
	}

	filesystem, err := iso9660.Read(file.New(input, true), info.Size(), 0, 0)
	if err != nil {
		return fmt.Errorf("read Alpine ISO: %w", err)
	}

	defer filesystem.Close()

	// One verified ISO supplies the entire installer kernel build.
	artifacts := [...]netbootArtifact{
		{name: "vmlinuz-virt", destination: kernel},
		{name: "initramfs-virt", destination: initrd},
		{name: "modloop-virt", destination: modloop},
	}

	for _, artifact := range artifacts {
		err = extractNetbootFile(filesystem, artifact)
		if err != nil {
			return fmt.Errorf("extract %s: %w", artifact.name, err)
		}
	}

	// Unlike netboot, the ISO initramfs omits the roots needed for HTTPS APK fetches.
	return appendNetbootCA(filesystem, initrd)
}

func extractNetbootFile(filesystem *iso9660.FileSystem, artifact netbootArtifact) error {
	input, err := filesystem.OpenFile("/boot/"+artifact.name, os.O_RDONLY)
	if err != nil {
		return err
	}

	defer input.Close()

	output, err := os.OpenFile(artifact.destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	defer output.Close()

	written, err := io.Copy(output, io.LimitReader(input, maxNetbootSize+1))
	if err != nil {
		return err
	}

	if written == 0 || written > maxNetbootSize {
		return fmt.Errorf("invalid artifact size %d", written)
	}

	return output.Close()
}

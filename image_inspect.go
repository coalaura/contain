package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
)

func imageAlpineVersion(path string) string {
	disk, err := os.Open(path)
	if err != nil {
		return "unknown"
	}

	defer disk.Close()

	info, err := disk.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "unknown"
	}

	filesystem, err := ext4.Read(file.New(disk, true), info.Size(), 0, 512)
	if err != nil {
		return "unknown"
	}

	defer filesystem.Close()

	release, err := filesystem.OpenFile("/etc/alpine-release", os.O_RDONLY)
	if err != nil {
		return "unknown"
	}

	defer release.Close()

	var buffer [128]byte

	count, err := io.ReadFull(release, buffer[:])
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "unknown"
	}

	version := strings.TrimSpace(string(buffer[:count]))
	if version == "" || version[0] < '0' || version[0] > '9' {
		return "unknown"
	}

	for index := range len(version) {
		character := version[index]

		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		separator := character == '.' || character == '-' || character == '_'

		if !letter && !digit && !separator {
			return "unknown"
		}
	}

	return version
}

func imageStatus(directory string) string {
	names := [...]string{"disk.raw", "vmlinuz-virt", "initramfs-virt"}

	for _, name := range names {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "incomplete"
		}
	}

	return "ready"
}

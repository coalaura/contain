package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
)

func parseDiskSize(value string) (int64, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return 0, errors.New("disk size cannot be empty")
	}

	multiplier := int64(1)

	unit := strings.IndexByte("KMGT", value[len(value)-1])
	if unit >= 0 {
		multiplier <<= 10 * (unit + 1)
		value = value[:len(value)-1]
	}

	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil || amount <= 0 || amount > math.MaxInt64/multiplier {
		return 0, errors.New("disk size must be a positive whole number of bytes, optionally followed by K, M, G or T")
	}

	size := amount * multiplier
	if size < 512<<20 {
		return 0, errors.New("disk size must be at least 512 MiB")
	}

	if size%4096 != 0 {
		return 0, errors.New("disk size must be a multiple of 4096 bytes")
	}

	return size, nil
}

func createDisk(path string, size int64) error {
	disk, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("create disk: %w", err)
	}

	defer disk.Close()

	err = os.Truncate(path, size)
	if err != nil {
		return fmt.Errorf("size disk: %w", err)
	}

	params := &ext4.Params{VolumeName: "alpine-root"}

	filesystem, err := ext4.Create(file.New(disk, false), size, 0, 512, params)
	if err != nil {
		return fmt.Errorf("format ext4 disk: %w", err)
	}

	err = filesystem.Close()
	if err != nil {
		return err
	}

	err = disk.Sync()
	if err != nil {
		return err
	}

	return disk.Close()
}

func collectBootFiles(directory string) error {
	boot := filepath.Join(directory, "boot")
	names := [...]string{"vmlinuz-virt", "initramfs-virt"}

	for _, name := range names {
		source := filepath.Join(boot, name)

		err := FilesExist(source)
		if err != nil {
			return fmt.Errorf("export %s: %w", name, err)
		}

		err = os.Rename(source, filepath.Join(directory, name))
		if err != nil {
			return err
		}
	}

	return os.Remove(boot)
}

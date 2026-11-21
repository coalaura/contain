package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
)

type mountInspection struct {
	status string
	err    error
}

func inspectMountTargets(diskPath string, mounts []GuestMount) []mountInspection {
	results := make([]mountInspection, len(mounts))

	if len(mounts) == 0 {
		return results
	}

	filesystem, closeDisk, diskErr := openGuestDisk(diskPath)
	if diskErr == nil {
		defer closeDisk()
	}

	for index := range mounts {
		mount := &mounts[index]
		result := &results[index]

		// Writable overrides cover the parent host export, not the guest disk.
		if mount.ParentPath != "" {
			result.status = "n/a (parent mount)"

			continue
		}

		if diskErr != nil {
			result.status = "unknown"
			result.err = diskErr

			continue
		}

		result.status, result.err = inspectGuestMount(filesystem, mount.GuestPath)
	}

	return results
}

func openGuestDisk(diskPath string) (*ext4.FileSystem, func(), error) {
	disk, err := os.Open(diskPath)
	if err != nil {
		return nil, nil, err
	}

	info, err := disk.Stat()
	if err != nil {
		disk.Close()

		return nil, nil, err
	}

	filesystem, err := ext4.Read(file.New(disk, true), info.Size(), 0, 512)
	if err != nil {
		disk.Close()

		return nil, nil, err
	}

	return filesystem, func() {
		filesystem.Close()
		disk.Close()
	}, nil
}

func inspectGuestMount(filesystem fs.ReadDirFS, guestPath string) (string, error) {
	directory := "."

	// Walk entries explicitly: the ext4 reader does not preserve fs.ErrNotExist.
	for component := range strings.SplitSeq(strings.TrimPrefix(guestPath, "/"), "/") {
		entries, err := filesystem.ReadDir(directory)
		if err != nil {
			return "unknown", err
		}

		var found fs.DirEntry

		for _, entry := range entries {
			if entry.Name() == component {
				found = entry

				break
			}
		}

		if found == nil {
			return "no (missing)", nil
		}

		directory = path.Join(directory, component)

		if !found.IsDir() {
			return "unknown", fmt.Errorf("/%s is not a directory (symlinks are not followed)", directory)
		}
	}

	pending := []string{directory}

	for len(pending) > 0 {
		directory = pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		entries, err := filesystem.ReadDir(directory)
		if err != nil {
			return "unknown", err
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				return "yes", nil
			}

			pending = append(pending, path.Join(directory, entry.Name()))
		}
	}

	return "no (empty)", nil
}

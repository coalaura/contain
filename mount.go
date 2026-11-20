package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

type GuestMount struct {
	Tag       string
	GuestPath string
	HostPath  string
	Readonly  bool
}

func ValidateGuestMounts(mounts []GuestMount) error {
	if len(mounts) == 0 {
		return errors.New("no guest mounts configured; need a workspace export")
	}

	if mounts[0].Tag != "workspace" || mounts[0].GuestPath != "/workspace" {
		return errors.New("first guest mount must use tag \"workspace\" at \"/workspace\"")
	}

	seenTags := make(map[string]bool, len(mounts))
	seenPaths := make(map[string]bool, len(mounts))

	for index := range mounts {
		mount := &mounts[index]

		if mount.Tag == "" || strings.Trim(mount.Tag, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.") != "" {
			return fmt.Errorf("invalid virtio-fs tag %q", mount.Tag)
		}

		if !path.IsAbs(mount.GuestPath) || path.Clean(mount.GuestPath) != mount.GuestPath || strings.ContainsRune(mount.GuestPath, '\x00') {
			return fmt.Errorf("invalid guest mount path %q", mount.GuestPath)
		}

		if index > 0 && path.Dir(mount.GuestPath) != "/projects" {
			return fmt.Errorf("additional guest mount path %q must be a direct child of /projects", mount.GuestPath)
		}

		if seenTags[mount.Tag] {
			return fmt.Errorf("duplicate guest mount tag %q", mount.Tag)
		}

		if seenPaths[mount.GuestPath] {
			return fmt.Errorf("duplicate guest mount path %q", mount.GuestPath)
		}

		seenTags[mount.Tag] = true
		seenPaths[mount.GuestPath] = true

		if mount.HostPath == "" || strings.ContainsAny(mount.HostPath, ",\x00") {
			return fmt.Errorf("invalid host path %q for virtio-fs tag %q: path must be nonempty and contain no commas or NUL", mount.HostPath, mount.Tag)
		}

		info, err := os.Stat(mount.HostPath)
		if err != nil {
			return fmt.Errorf("stat host path for virtio-fs tag %q: %w", mount.Tag, err)
		}

		if !info.IsDir() {
			return fmt.Errorf("host path %q for virtio-fs tag %q is not a directory", mount.HostPath, mount.Tag)
		}
	}

	return nil
}

package main

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

type GuestMount struct {
	Tag       string
	GuestPath string
}

func validateGuestMounts(mounts []GuestMount) error {
	if len(mounts) == 0 {
		return errors.New("no guest mounts configured; need a workspace export")
	}

	var (
		seenTags  = make(map[string]bool)
		seenPaths = make(map[string]bool)
		workspace bool
	)

	for _, m := range mounts {
		if m.Tag == "" || strings.Trim(m.Tag, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.") != "" {
			return fmt.Errorf("invalid virtio-fs tag %q", m.Tag)
		}

		if !path.IsAbs(m.GuestPath) || path.Clean(m.GuestPath) != m.GuestPath || strings.ContainsRune(m.GuestPath, '\x00') {
			return fmt.Errorf("invalid guest mount path %q", m.GuestPath)
		}

		if m.GuestPath != "/workspace" && (!strings.HasPrefix(m.GuestPath, "/projects/") || strings.Contains(strings.TrimPrefix(m.GuestPath, "/projects/"), "/")) {
			return fmt.Errorf("guest mount %q must be /workspace or /projects/<name>", m.GuestPath)
		}

		if seenTags[m.Tag] || seenPaths[m.GuestPath] {
			return fmt.Errorf("duplicate guest mount tag %q or path %q", m.Tag, m.GuestPath)
		}

		seenTags[m.Tag] = true
		seenPaths[m.GuestPath] = true

		if m.GuestPath == "/workspace" {
			workspace = true
		}
	}

	if !workspace {
		return errors.New("missing /workspace mount")
	}

	return nil
}

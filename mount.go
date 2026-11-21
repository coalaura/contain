package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	guestProjectsRoot = "/projects"
	guestConfigRoot   = "~/.config"
)

type GuestMount struct {
	Tag        string
	GuestPath  string
	HostPath   string
	ParentPath string
	Readonly   bool
}

func ValidateGuestMounts(mounts []GuestMount) error {
	if len(mounts) == 0 {
		return errors.New("no guest mounts configured; need a workspace export")
	}

	if mounts[0].Tag != "workspace" || mounts[0].GuestPath != "/workspace" {
		return errors.New("first guest mount must use tag \"workspace\" at \"/workspace\"")
	}

	seenTags := make(map[string]bool, len(mounts))
	seenPaths := make(map[string]*GuestMount, len(mounts))

	for index := range mounts {
		mount := &mounts[index]

		if !validMountIdentifier(mount.Tag) {
			return fmt.Errorf("invalid virtio-fs tag %q", mount.Tag)
		}

		configPath := strings.HasPrefix(mount.GuestPath, guestConfigRoot+"/")
		if (!path.IsAbs(mount.GuestPath) && !configPath) || path.Clean(mount.GuestPath) != mount.GuestPath || strings.ContainsRune(mount.GuestPath, '\x00') {
			return fmt.Errorf("invalid guest mount path %q", mount.GuestPath)
		}

		if mount.ParentPath == "" && index > 0 {
			root := path.Dir(mount.GuestPath)
			if root != guestProjectsRoot && root != guestConfigRoot {
				return fmt.Errorf("additional guest mount path %q must be a direct child of /projects or ~/.config", mount.GuestPath)
			}
		}

		if seenTags[mount.Tag] {
			return fmt.Errorf("duplicate guest mount tag %q", mount.Tag)
		}

		if seenPaths[mount.GuestPath] != nil {
			return fmt.Errorf("duplicate guest mount path %q", mount.GuestPath)
		}

		if mount.ParentPath != "" {
			err := validateWritableMount(mount, seenPaths[mount.ParentPath])
			if err != nil {
				return err
			}

			for previous := range index {
				other := &mounts[previous]
				if other.ParentPath != mount.ParentPath {
					continue
				}

				if guestPathWithin(mount.GuestPath, other.GuestPath) || guestPathWithin(other.GuestPath, mount.GuestPath) {
					return fmt.Errorf("overlapping writable paths %q and %q", other.GuestPath, mount.GuestPath)
				}
			}
		}

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

		seenTags[mount.Tag] = true
		seenPaths[mount.GuestPath] = mount
	}

	return nil
}

func validateWritableMount(mount, parent *GuestMount) error {
	if parent == nil || parent.ParentPath != "" || !parent.Readonly {
		return fmt.Errorf("writable mount %q requires a preceding read-only parent mount", mount.GuestPath)
	}

	if mount.Readonly {
		return fmt.Errorf("writable mount %q must be read/write", mount.GuestPath)
	}

	relative, found := strings.CutPrefix(mount.GuestPath, parent.GuestPath+"/")
	if !found {
		return fmt.Errorf("writable mount %q must be inside parent %q", mount.GuestPath, parent.GuestPath)
	}

	local, err := localWritablePath(relative)
	if err != nil {
		return err
	}

	expected := filepath.Join(parent.HostPath, local)
	if filepath.Clean(mount.HostPath) != expected {
		return fmt.Errorf("writable mount %q must export host subdirectory %q", mount.GuestPath, expected)
	}

	current := parent.HostPath

	for component := range strings.SplitSeq(relative, "/") {
		current = filepath.Join(current, component)

		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect writable host path %q: %w", current, err)
		}

		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("writable host path %q must be an existing directory without symbolic links", current)
		}
	}

	return nil
}

func localWritablePath(relative string) (string, error) {
	if relative == "." || strings.ContainsRune(relative, '\\') {
		return "", fmt.Errorf("invalid writable path %q: expected a relative directory path without traversal", relative)
	}

	for index := range len(relative) {
		if relative[index] < ' ' || relative[index] == '\x7f' {
			return "", fmt.Errorf("invalid writable path %q: control characters are unsupported", relative)
		}
	}

	local, err := filepath.Localize(relative)
	if err != nil {
		return "", fmt.Errorf("invalid writable path %q: %w", relative, err)
	}

	if filepath.Separator == '\\' {
		for component := range strings.SplitSeq(relative, "/") {
			// Win32 normalization must not turn a component into an alias or traversal.
			last := component[len(component)-1]
			if last == '.' || last == ' ' {
				return "", fmt.Errorf("invalid writable path %q: Windows directory names cannot end in dots or spaces", relative)
			}
		}
	}

	return local, nil
}

func guestPathWithin(value, parent string) bool {
	suffix, found := strings.CutPrefix(value, parent)
	return found && (suffix == "" || suffix[0] == '/')
}

func validMountIdentifier(name string) bool {
	if name == "" {
		return false
	}

	for index := 0; index < len(name); index++ {
		character := name[index]

		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		punctuation := character == '_' || character == '.' || character == '-'

		if !letter && !digit && !punctuation {
			return false
		}
	}

	return true
}

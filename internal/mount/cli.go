package mount

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/coalaura/contain/internal/pathutil"
)

func Parse(value string) (Config, error) {
	mount := Config{Host: value}
	name := ""
	volume := filepath.VolumeName(value)
	offset := 0

	// A Windows drive colon belongs to the host path, including extended paths.
	if len(volume) >= 2 && volume[len(volume)-1] == ':' {
		drive := volume[len(volume)-2]
		if drive >= 'a' && drive <= 'z' || drive >= 'A' && drive <= 'Z' {
			offset = len(volume)
		}
	}

	separator := strings.IndexByte(value[offset:], ':')
	if separator >= 0 {
		separator += offset
		mount.Host = value[:separator]
		options := value[separator+1:]

		if options == "" {
			return Config{}, errors.New("expected a mount name or ro/rw after ':'")
		}

		var (
			mode         string
			explicitMode bool
		)

		name, mode, explicitMode = strings.Cut(options, ":")
		if !explicitMode && (name == "ro" || name == "rw") {
			mode = name
			name = ""
		}

		if (explicitMode || mode != "") && mode != "ro" && mode != "rw" {
			return Config{}, fmt.Errorf("invalid mount mode %q: expected ro or rw", mode)
		}

		mount.Readonly = mode == "ro"
	}

	if mount.Host == "" {
		return Config{}, errors.New("host directory cannot be empty")
	}

	host, err := pathutil.Host(mount.Host)
	if err != nil {
		return Config{}, fmt.Errorf("resolve host directory: %w", err)
	}

	mount.Host = host

	if name == "" {
		name = filepath.Base(host)
		if !validMountName(name) {
			return Config{}, fmt.Errorf("cannot infer a valid mount name from %q: use HOST:NAME[:ro|rw]", host)
		}
	}

	if !validMountName(name) {
		return Config{}, fmt.Errorf("invalid mount name %q: use ASCII letters, digits, dots, underscores or hyphens, except '.' and '..'", name)
	}

	mount.Guest = guestProjectsRoot + "/" + name

	return mount, nil
}

func validMountName(name string) bool {
	return name != "." && name != ".." && validMountIdentifier(name)
}

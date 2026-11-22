package pathutil

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const GuestHome = "/root"

func Host(value string) (string, error) {
	if value == "" {
		return "", errors.New("host path cannot be empty")
	}

	if value[0] == '~' {
		if len(value) > 1 && !os.IsPathSeparator(value[1]) {
			return "", errors.New("home paths must use ~ alone or followed by a path separator")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve host home: %w", err)
		}

		value = filepath.Join(home, value[1:])
	}

	return filepath.Abs(value)
}

func Guest(value string) (string, error) {
	absolute := path.IsAbs(value) || strings.HasPrefix(value, "~/")

	control := strings.ContainsFunc(value, func(character rune) bool {
		return character < ' ' || character == '\x7f'
	})

	if !absolute || path.Clean(value) != value || value == "/" || control {
		return "", errors.New("must be a clean absolute guest path or ~/... without control characters")
	}

	if strings.HasPrefix(value, "~/") {
		return GuestHome + value[1:], nil
	}

	return value, nil
}

func GuestWithin(value, parent string) bool {
	suffix, found := strings.CutPrefix(value, parent)
	return found && (suffix == "" || suffix[0] == '/')
}

package network

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func ValidateShares(dataDirectory string, directories, files []string) error {
	auth, err := networkAuthPath()
	if err != nil {
		return err
	}

	protected := []string{filepath.Join(dataDirectory, "network-ca"), filepath.Join(dataDirectory, "network-ca", "authority.pem"), auth}

	for _, filename := range protected {
		private, pathErr := canonicalNetworkPath(filename)
		if pathErr != nil {
			return pathErr
		}

		for _, directory := range directories {
			mounted, mountErr := canonicalNetworkPath(directory)
			if mountErr != nil {
				return mountErr
			}

			if networkPathsContain(mounted, private) || networkPathsContain(private, mounted) {
				return fmt.Errorf("mount %q exposes host-only network credentials or CA material", directory)
			}
		}

		for _, file := range files {
			source, sourceErr := canonicalNetworkPath(file)
			if sourceErr != nil {
				return sourceErr
			}

			if networkPathsContain(private, source) || sameNetworkFile(filename, file) {
				return fmt.Errorf("snapshot %q exposes host-only network credentials or CA material", file)
			}
		}
	}

	return nil
}

func canonicalNetworkPath(filename string) (string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", err
	}

	resolved, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return "", err
		}

		resolved, err = canonicalNetworkPath(parent)
		resolved = filepath.Join(resolved, filepath.Base(absolute))
	}

	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}

	return resolved, err
}

func networkPathsContain(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func sameNetworkFile(first, second string) bool {
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)

	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

package network

import (
	"errors"
	"os"
)

func protectNetworkDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}

	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("network private directory must not be a symlink")
	}

	return os.Chmod(directory, 0700)
}

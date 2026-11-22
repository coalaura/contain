package network

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func tryOAuthLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}

	return err == nil, err
}

func protectOAuthFile(filename string) error {
	return os.Chmod(filename, 0600)
}

func replaceOAuthFile(source, destination string) error {
	err := os.Rename(source, destination)
	if err != nil {
		return err
	}

	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}

	defer directory.Close()

	return directory.Sync()
}

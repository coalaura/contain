package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Paths struct {
	WorkingDirectory string
	DataDirectory    string
	ConfigFile       string
	OpenVMM          string
}

func ResolvePaths() (*Paths, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(home, ".local", "contain")

	_, err = os.Stat(dataDir)
	if os.IsNotExist(err) {
		err = os.MkdirAll(dataDir, 0755)
		if err != nil {
			return nil, err
		}
	}

	configDir := filepath.Join(home, ".config")

	_, err = os.Stat(configDir)
	if os.IsNotExist(err) {
		err = os.MkdirAll(configDir, 0755)
		if err != nil {
			return nil, err
		}
	}

	bin, err := exec.LookPath("openvmm")
	if err != nil {
		return nil, err
	}

	return &Paths{
		WorkingDirectory: cwd,
		DataDirectory:    dataDir,
		ConfigFile:       filepath.Join(configDir, "contain.yml"),
		OpenVMM:          bin,
	}, nil
}

func FilesExist(files ...string) error {
	for _, file := range files {
		stat, err := os.Stat(file)
		if os.IsNotExist(err) {
			return fmt.Errorf("%q does not exist", file)
		} else if err != nil {
			return err
		}

		if stat.IsDir() {
			return fmt.Errorf("%q is a directory", file)
		} else if stat.Size() == 0 {
			return fmt.Errorf("%q is empty", file)
		}
	}

	return nil
}

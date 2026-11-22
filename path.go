package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

type Paths struct {
	WorkingDirectory string
	DataDirectory    string
	ImagesDirectory  string
	LogsDirectory    string
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
	logsDir := filepath.Join(dataDir, "logs")

	err = os.MkdirAll(logsDir, 0700)
	if err != nil {
		return nil, err
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
		ImagesDirectory:  filepath.Join(dataDir, "images"),
		LogsDirectory:    logsDir,
		ConfigFile:       filepath.Join(configDir, "contain.yml"),
		OpenVMM:          bin,
	}, nil
}

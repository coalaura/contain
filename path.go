package main

import (
	"os"
	"path/filepath"
)

type Paths struct {
	DataDirectory string
	ConfigFile    string
}

func ResolvePaths() (*Paths, error) {
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

	return &Paths{
		DataDirectory: dataDir,
		ConfigFile:    filepath.Join(configDir, "contain.yml"),
	}, nil
}

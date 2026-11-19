package main

import (
	"bytes"
	"os"

	"github.com/goccy/go-yaml"
)

type Config struct {
	// TODO
}

func (c *Config) Init() error {
	var store bool

	// initialize

	if store {
		err := c.Store()
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) Store() error {
	var (
		buffer   bytes.Buffer
		comments = yaml.CommentMap{
			"$.example": {yaml.HeadComment(" example config entry")},
		}
	)

	err := yaml.NewEncoder(&buffer, yaml.WithComment(comments)).Encode(c)
	if err != nil {
		return err
	}

	body := bytes.ReplaceAll(buffer.Bytes(), []byte("#\n"), []byte("\n"))

	return os.WriteFile(paths.ConfigFile, body, 0600)
}

func DefaultConfig() *Config {
	return &Config{}
}

func LoadConfig() (*Config, error) {
	cfg := DefaultConfig()

	file, err := os.OpenFile(paths.ConfigFile, os.O_RDONLY, 0)
	if err == nil {
		defer file.Close()

		info, err := file.Stat()
		if err != nil {
			return nil, err
		}

		if info.Mode() != 0600 {
			err = os.Chmod(paths.ConfigFile, 0600)
			if err != nil {
				return nil, err
			}
		}

		err = yaml.NewDecoder(file).Decode(cfg)
		if err != nil {
			return nil, err
		}
	} else {
		if !os.IsNotExist(err) {
			return nil, err
		}

		err = cfg.Store()
		if err != nil {
			return nil, err
		}
	}

	err = cfg.Init()
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/coalaura/contain/internal/mount"
	"github.com/coalaura/contain/internal/network"
	"github.com/coalaura/contain/internal/pathutil"
	"github.com/coalaura/contain/internal/snapshot"

	"github.com/goccy/go-yaml"
)

type ConfigLogin struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type MachineConfig struct {
	disk   string
	initrd string
	kernel string

	Source     string         `yaml:"source"`
	Processors int            `yaml:"processors"`
	Memory     int            `yaml:"memory"`
	Login      ConfigLogin    `yaml:"login"`
	Command    string         `yaml:"command"`
	Network    network.Config `yaml:"network"`
}

type Config struct {
	Machine   MachineConfig     `yaml:"machine"`
	Mounts    []mount.Config    `yaml:"mounts"`
	Snapshots []snapshot.Config `yaml:"snapshots"`
}

var MaxProcs = runtime.GOMAXPROCS(0)

func (c *Config) Init() error {
	networkErr := c.Machine.Network.Validate()
	if networkErr != nil {
		return networkErr
	}

	// validate processor count
	if c.Machine.Processors > MaxProcs {
		return fmt.Errorf("machine.processors can be at most %d", MaxProcs)
	} else if c.Machine.Processors < 1 {
		return errors.New("machine.processors has to be 1 or more")
	}

	// validate memory size
	if c.Machine.Memory > 32 {
		return errors.New("machine.memory can be at most 32G")
	} else if c.Machine.Memory < 1 {
		return errors.New("machine.memory has to be 1G or more")
	}

	// validate guest image files
	directory, err := pathutil.Host(c.Machine.Source)
	if err != nil {
		return fmt.Errorf("machine.source: %w", err)
	}

	disk := filepath.Join(directory, "disk.raw")
	initrd := filepath.Join(directory, "initramfs-virt")
	kernel := filepath.Join(directory, "vmlinuz-virt")

	err = pathutil.FilesExist(disk, initrd, kernel)
	if err != nil {
		return err
	}

	c.Machine.disk = disk
	c.Machine.initrd = initrd
	c.Machine.kernel = kernel

	return nil
}

func (c *Config) Store() error {
	var (
		buffer   bytes.Buffer
		comments = yaml.CommentMap{
			"$.mounts":    {yaml.HeadComment(""), yaml.HeadComment(" additional mounts: host, guest and optional readonly (default false)"), yaml.HeadComment(" guest: a direct child of /projects, ~/.config, ~/.local, ~/.local/share, ~/.local/state or ~/.local/bin"), yaml.HeadComment(" writable lists existing relative subdirectories exported read/write; requires readonly: true")},
			"$.snapshots": {yaml.HeadComment(""), yaml.HeadComment(" files copied once per session: host and guest (absolute path or ~/...; guest ~ means /root)"), yaml.HeadComment(" replaces guest files with private (0600) copies; destinations overlapping host mounts are rejected")},

			"$.machine.source":     {yaml.HeadComment(" path to the guest image directory (supports ~)")},
			"$.machine.processors": {yaml.HeadComment(" amount of processors to assign to the vm")},
			"$.machine.memory":     {yaml.HeadComment(" amount of memory to assign to the vm (in GiB)")},
			"$.machine.command":    {yaml.HeadComment(" optional single-line shell command in /workspace; shuts down the vm on exit (skipped with --shell)")},
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
	return &Config{
		Machine: MachineConfig{
			Source:     filepath.Join(paths.DataDirectory, "images", "alpine"),
			Processors: max(1, min(4, MaxProcs-2)),
			Memory:     4,
			Network:    network.DefaultConfig(),
			Login: ConfigLogin{
				Username: "root",
				Password: "alpine",
			},
		},
		Mounts:    []mount.Config{},
		Snapshots: []snapshot.Config{},
	}
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

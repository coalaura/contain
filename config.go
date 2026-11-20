package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/goccy/go-yaml"
)

type ConfigLogin struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type MachineConfig struct {
	Processors int         `yaml:"processors"`
	Memory     int         `yaml:"memory"`
	Login      ConfigLogin `yaml:"login"`
	Command    string      `yaml:"command"`
}

type MountConfig struct {
	Name     string `yaml:"name"`
	Host     string `yaml:"host"`
	Readonly bool   `yaml:"readonly"`
}

type SourceConfig struct {
	disk   string
	initrd string
	kernel string

	Alpine string `yaml:"alpine"`
}

type Config struct {
	Machine MachineConfig `yaml:"machine"`
	Source  SourceConfig  `yaml:"source"`
	Mounts  []MountConfig `yaml:"mounts"`
}

var MaxProcs = runtime.GOMAXPROCS(0)

func (c *Config) Init() error {
	// validate processor count
	if c.Machine.Processors > MaxProcs {
		return fmt.Errorf("machine.processors can be at most %d", MaxProcs)
	} else if c.Machine.Processors < 1 {
		return errors.New("machine.processors has to be 1 or more")
	}

	// validate memory size
	if c.Machine.Memory > 32 {
		return errors.New("machine.memory can be at most 32G")
	} else if c.Machine.Processors < 1 {
		return errors.New("machine.memory has to be 1G or more")
	}

	for _, character := range c.Machine.Command {
		if character < ' ' || character == '\x7f' {
			return errors.New("machine.command must be a single-line shell command without control characters")
		}
	}

	// validate alpine build files
	disk := filepath.Join(c.Source.Alpine, "disk.raw")
	initrd := filepath.Join(c.Source.Alpine, "initramfs-virt")
	kernel := filepath.Join(c.Source.Alpine, "vmlinuz-virt")

	err := FilesExist(disk, initrd, kernel)
	if err != nil {
		return err
	}

	c.Source.disk = disk
	c.Source.initrd = initrd
	c.Source.kernel = kernel

	return nil
}

func (c *Config) GuestMounts(workspace string) ([]GuestMount, error) {
	mounts := make([]GuestMount, len(c.Mounts)+1)

	mounts[0] = GuestMount{
		Tag:       "workspace",
		GuestPath: "/workspace",
		HostPath:  workspace,
	}

	for index := range c.Mounts {
		mount := &c.Mounts[index]

		if !validMountName(mount.Name) {
			return nil, fmt.Errorf("mounts[%d].name must contain only ASCII letters, digits, dots, underscores or hyphens, and cannot be empty, '.' or '..'", index)
		}

		mounts[index+1] = GuestMount{
			Tag:       "project" + strconv.Itoa(index),
			GuestPath: "/projects/" + mount.Name,
			HostPath:  mount.Host,
			Readonly:  mount.Readonly,
		}
	}

	err := ValidateGuestMounts(mounts)
	if err != nil {
		return nil, err
	}

	return mounts, nil
}

func (c *Config) Store() error {
	var (
		buffer   bytes.Buffer
		comments = yaml.CommentMap{
			"$.source": {yaml.HeadComment("")},

			"$.machine.processors": {yaml.HeadComment(" amount of processors to assign to the vm")},
			"$.machine.memory":     {yaml.HeadComment(" amount of memory to assign to the vm (in GiB)")},
			"$.machine.command":    {yaml.HeadComment(" optional single-line shell command to start in /workspace before terminal handoff")},

			"$.source.alpine": {yaml.HeadComment(" path to the alpine build directory")},
			"$.mounts":        {yaml.HeadComment(""), yaml.HeadComment(" additional mounts: name (under /projects), host, and optional readonly (default false)")},
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
			Processors: max(1, min(4, MaxProcs-2)),
			Memory:     4,
			Login: ConfigLogin{
				Username: "root",
				Password: "alpine",
			},
		},
		Source: SourceConfig{
			Alpine: "/path/to/alpine/build",
		},
		Mounts: []MountConfig{},
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

func validMountName(name string) bool {
	return name != "." && name != ".." && validMountIdentifier(name)
}

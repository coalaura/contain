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
	"github.com/coalaura/contain/internal/sessionlog"
	"github.com/coalaura/contain/internal/snapshot"

	"github.com/goccy/go-yaml"
)

type ConfigLogin struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type MachineConfig struct {
	directory string
	disk      string
	initrd    string
	kernel    string

	Processors int               `yaml:"processors"`
	Memory     int               `yaml:"memory"`
	Command    string            `yaml:"command"`
	Login      ConfigLogin       `yaml:"login"`
	Network    network.Config    `yaml:"network"`
	DNS        network.DNSConfig `yaml:"dns"`
}

type Config struct {
	Machine   MachineConfig     `yaml:"machine"`
	Logs      sessionlog.Config `yaml:"logs"`
	Mounts    []mount.Config    `yaml:"mounts"`
	Snapshots []snapshot.Config `yaml:"snapshots"`
}

var MaxProcs = runtime.GOMAXPROCS(0)

func (c *Config) Init(image string) error {
	err := validateImageName(image)
	if err != nil {
		return err
	}

	logErr := c.Logs.Validate()
	if logErr != nil {
		return logErr
	}

	networkErr := c.Machine.Network.Validate()
	if networkErr != nil {
		return networkErr
	}

	dnsErr := c.Machine.DNS.Validate()
	if dnsErr != nil {
		return dnsErr
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
	directory := filepath.Join(paths.ImagesDirectory, image)
	disk := filepath.Join(directory, "disk.raw")
	initrd := filepath.Join(directory, "initramfs-virt")
	kernel := filepath.Join(directory, "vmlinuz-virt")

	err = pathutil.FilesExist(disk, initrd, kernel)
	if err != nil {
		return fmt.Errorf("image %q: %w", image, err)
	}

	c.Machine.directory = directory
	c.Machine.disk = disk
	c.Machine.initrd = initrd
	c.Machine.kernel = kernel

	return nil
}

func (c *Config) Store() error {
	var (
		buffer   bytes.Buffer
		comments = yaml.CommentMap{
			"$.logs":              {yaml.HeadComment("", " session logs keep the current file and one previous file")},
			"$.logs.max_size_mib": {yaml.HeadComment(" maximum size per log file in MiB (1-1024)")},

			"$.machine.processors": {yaml.HeadComment(" amount of processors to assign to the vm")},
			"$.machine.memory":     {yaml.HeadComment(" amount of memory to assign to the vm (in GiB)")},
			"$.machine.command":    {yaml.HeadComment(" optional single-line shell command in /workspace/{basename}; shuts down the vm on exit (skipped with --shell)")},

			"$.machine.login":          {yaml.HeadComment("")},
			"$.machine.login.username": {yaml.HeadComment(" guest console login username")},
			"$.machine.login.password": {yaml.HeadComment(" guest console login password")},

			"$.machine.network":                {yaml.HeadComment("")},
			"$.machine.network.mode":           {yaml.HeadComment(" open (no IP/domain rules), isolated (IP/domain rules) or closed (no networking)")},
			"$.machine.network.default":        {yaml.HeadComment(" action for unmatched IPs and DNS names in isolated mode: allow or deny")},
			"$.machine.network.allow":          {yaml.HeadComment(" IP addresses, CIDRs or exact domain names allowed in isolated mode")},
			"$.machine.network.deny":           {yaml.HeadComment(" IP addresses, CIDRs or exact domain names blocked in isolated mode; overrides allow")},
			"$.machine.network.credentials":    {yaml.HeadComment(" none (or empty), or opencode to inject host credentials in isolated mode")},
			"$.machine.network.allow_loopback": {yaml.HeadComment(" allow host addresses, including loopback; still subject to network rules")},
			"$.machine.network.allow_lan":      {yaml.HeadComment(" allow private and directly connected networks; still subject to network rules")},

			"$.machine.dns":           {yaml.HeadComment("")},
			"$.machine.dns.primary":   {yaml.HeadComment(" host resolver upstream: IP:port, domain:port or https://... DoH URL")},
			"$.machine.dns.secondary": {yaml.HeadComment(" fallback DNS upstream; empty disables fallback")},

			"$.mounts":    {yaml.HeadComment("", " additional mounts: host, guest and optional readonly (default false)", " guest: a direct child of /projects, ~/.config, ~/.local, ~/.local/share, ~/.local/state or ~/.local/bin", " writable lists existing relative subdirectories exported read/write; requires readonly: true")},
			"$.snapshots": {yaml.HeadComment("", " files copied once per session: host and guest (absolute path or ~/...; guest ~ means /root)", " replaces guest files with private (0600) copies; destinations overlapping host mounts are rejected")},
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
			Network:    network.DefaultConfig(),
			DNS:        network.DefaultDNSConfig(),
			Login: ConfigLogin{
				Username: "root",
				Password: "alpine",
			},
		},
		Logs: sessionlog.Config{
			MaxSizeMiB: sessionlog.DefaultMaxSizeMiB,
		},
		Mounts:    []mount.Config{},
		Snapshots: []snapshot.Config{},
	}
}

func LoadConfig(image string) (*Config, error) {
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

	err = cfg.Init(image)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

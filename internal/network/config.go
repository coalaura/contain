package network

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Mode          string   `yaml:"mode"`
	Default       string   `yaml:"default"`
	Allow         []string `yaml:"allow"`
	Deny          []string `yaml:"deny"`
	Credentials   string   `yaml:"credentials,omitempty"`
	AllowLoopback bool     `yaml:"allow_loopback"`
	AllowLAN      bool     `yaml:"allow_lan"`
}

type networkPolicy struct {
	mode          string
	defaultAllow  bool
	allow         []netip.Prefix
	deny          []netip.Prefix
	allowLoopback bool
	allowLAN      bool
	inspectHTTPS  bool
	local         networkLocalAccess
}

type networkConfigValue Config

// Decode this security boundary strictly, without changing unrelated config parsing.
func (config *Config) UnmarshalYAML(data []byte) error {
	value := DefaultConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data), yaml.Strict())

	// An alias prevents recursively invoking this method.
	decoded := networkConfigValue(value)

	err := decoder.Decode(&decoded)
	if err != nil {
		return err
	}

	*config = Config(decoded)

	_, err = compileNetworkPolicy(*config)
	return err
}

func (policy *networkPolicy) permits(address netip.Addr) bool {
	address = address.Unmap()
	if policy.mode == "closed" || !address.IsValid() {
		return false
	}

	if !policy.permitsLocal(address) {
		return false
	}

	if policy.mode == "open" {
		return true
	}

	for _, prefix := range policy.deny {
		if prefix.Contains(address) {
			return false
		}
	}

	if policy.defaultAllow {
		return true
	}

	for _, prefix := range policy.allow {
		if prefix.Contains(address) {
			return true
		}
	}

	return false
}

func (config Config) Validate() error {
	_, err := compileNetworkPolicy(config)
	return err
}

func (config Config) InspectHTTPS() bool {
	return config.Mode == "isolated" && config.Credentials != ""
}

func DefaultConfig() Config {
	return Config{Mode: "isolated", Default: "deny", Allow: []string{}, Deny: []string{}}
}

func compileNetworkPolicy(config Config) (*networkPolicy, error) {
	if config.Mode != "open" && config.Mode != "isolated" && config.Mode != "closed" {
		return nil, errors.New("machine.network.mode must be open, isolated or closed")
	}

	if config.Default != "allow" && config.Default != "deny" {
		return nil, errors.New("machine.network.default must be allow or deny")
	}

	if config.Credentials != "" && config.Credentials != "opencode" {
		return nil, errors.New("machine.network.credentials must be opencode or omitted")
	}

	allow, err := networkPrefixes(config.Allow)
	if err != nil {
		return nil, fmt.Errorf("machine.network.allow: %w", err)
	}

	deny, err := networkPrefixes(config.Deny)
	if err != nil {
		return nil, fmt.Errorf("machine.network.deny: %w", err)
	}

	return &networkPolicy{
		mode:          config.Mode,
		defaultAllow:  config.Default == "allow",
		allow:         allow,
		deny:          deny,
		allowLoopback: config.AllowLoopback,
		allowLAN:      config.AllowLAN,
		inspectHTTPS:  config.InspectHTTPS(),
	}, nil
}

func networkPrefixes(values []string) ([]netip.Prefix, error) {
	if len(values) > 4096 {
		return nil, errors.New("at most 4096 IP/CIDR rules are supported")
	}

	prefixes := make([]netip.Prefix, 0, len(values))

	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err == nil {
			if address.Zone() != "" {
				return nil, fmt.Errorf("scoped IP address %q is unsupported", value)
			}

			address = address.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))

			continue
		}

		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			return nil, fmt.Errorf("%q must be an IP address or canonical IPv4/IPv6 CIDR (no host bits or mapped IPv6 CIDRs)", value)
		}

		prefixes = append(prefixes, prefix)
	}

	return prefixes, nil
}

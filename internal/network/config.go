package network

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"strings"

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
	Ports         []string `yaml:"ports"`
}

type networkPolicy struct {
	mode          string
	defaultAllow  bool
	allow         []netip.Prefix
	deny          []netip.Prefix
	allowDomains  map[string]struct{}
	denyDomains   map[string]struct{}
	dns           *dnsCache
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

	return policy.dns != nil && policy.dns.permits(address)
}

func (config Config) Validate() error {
	_, err := compileNetworkPolicy(config)
	return err
}

func (config Config) InspectHTTPS() bool {
	return (config.Mode == "isolated" || config.Mode == "open") && config.Credentials == "opencode"
}

func DefaultConfig() Config {
	return Config{Mode: "isolated", Default: "deny", Allow: []string{}, Deny: []string{}, Credentials: "none", Ports: []string{}}
}

func compileNetworkPolicy(config Config) (*networkPolicy, error) {
	if config.Mode != "open" && config.Mode != "isolated" && config.Mode != "closed" {
		return nil, errors.New("machine.network.mode must be open, isolated or closed")
	}

	if config.Default != "allow" && config.Default != "deny" {
		return nil, errors.New("machine.network.default must be allow or deny")
	}

	if config.Credentials != "" && config.Credentials != "none" && config.Credentials != "opencode" {
		return nil, errors.New("machine.network.credentials must be none, opencode or empty")
	}

	_, err := ResolvePorts(config.Ports)
	if err != nil {
		return nil, err
	}

	if config.Mode == "closed" && len(config.Ports) != 0 {
		return nil, errors.New("published ports require machine.network.mode open or isolated")
	}

	allow, allowDomains, err := networkRules(config.Allow)
	if err != nil {
		return nil, fmt.Errorf("machine.network.allow: %w", err)
	}

	deny, denyDomains, err := networkRules(config.Deny)
	if err != nil {
		return nil, fmt.Errorf("machine.network.deny: %w", err)
	}

	return &networkPolicy{
		mode:          config.Mode,
		defaultAllow:  config.Default == "allow",
		allow:         allow,
		deny:          deny,
		allowDomains:  allowDomains,
		denyDomains:   denyDomains,
		allowLoopback: config.AllowLoopback,
		allowLAN:      config.AllowLAN,
		inspectHTTPS:  config.InspectHTTPS(),
	}, nil
}

func networkRules(values []string) ([]netip.Prefix, map[string]struct{}, error) {
	if len(values) > 4096 {
		return nil, nil, errors.New("at most 4096 IP/CIDR/domain rules are supported")
	}

	prefixes := make([]netip.Prefix, 0, len(values))
	domains := make(map[string]struct{}, len(values))

	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err == nil {
			if address.Zone() != "" {
				return nil, nil, fmt.Errorf("scoped IP address %q is unsupported", value)
			}

			address = address.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))

			continue
		}

		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			domain, valid := networkDomain(value)
			if !valid {
				return nil, nil, fmt.Errorf("%q must be an IP address, canonical IPv4/IPv6 CIDR or exact ASCII domain name", value)
			}

			domains[domain] = struct{}{}

			continue
		}

		prefixes = append(prefixes, prefix)
	}

	return prefixes, domains, nil
}

func networkDomain(value string) (string, bool) {
	value = strings.TrimSuffix(value, ".")
	if len(value) == 0 || len(value) > 253 {
		return "", false
	}

	hasLetter := false

	for label := range strings.SplitSeq(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}

		for _, character := range label {
			letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
			if !letter && (character < '0' || character > '9') && character != '-' && character != '_' {
				return "", false
			}

			hasLetter = hasLetter || letter
		}
	}

	return strings.ToLower(value), hasLetter
}

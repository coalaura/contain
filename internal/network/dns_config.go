package network

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

type DNSConfig struct {
	Primary   string `yaml:"primary"`
	Secondary string `yaml:"secondary"`
}

type dnsConfigValue DNSConfig

func (config *DNSConfig) UnmarshalYAML(data []byte) error {
	decoded := dnsConfigValue(DefaultDNSConfig())
	decoder := yaml.NewDecoder(bytes.NewReader(data), yaml.Strict())

	err := decoder.Decode(&decoded)
	if err != nil {
		return err
	}

	*config = DNSConfig(decoded)

	return config.Validate()
}

func (config DNSConfig) Validate() error {
	err := validateDNSUpstream(config.Primary)
	if err != nil {
		return fmt.Errorf("machine.dns.primary: %w", err)
	}

	if config.Secondary != "" {
		err = validateDNSUpstream(config.Secondary)
		if err != nil {
			return fmt.Errorf("machine.dns.secondary: %w", err)
		}
	}

	return nil
}

func DefaultDNSConfig() DNSConfig {
	return DNSConfig{Primary: "1.1.1.1:53", Secondary: "[2606:4700:4700::1111]:53"}
}

func validateDNSUpstream(value string) error {
	if strings.HasPrefix(value, "https://") {
		endpoint, err := url.Parse(value)
		if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.Opaque != "" {
			return errors.New("expected an HTTPS DNS-over-HTTPS URL without credentials or a fragment")
		}

		port := endpoint.Port()
		if port == "" {
			port = "443"
		}

		return validateDNSUpstream(net.JoinHostPort(endpoint.Hostname(), port))
	}

	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("expected IP:port, domain:port or an HTTPS DNS-over-HTTPS URL")
	}

	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("DNS port must be between 1 and 65535")
	}

	address, err := netip.ParseAddr(host)
	if err == nil {
		if address.Zone() != "" || !networkDestination(address) {
			return errors.New("unsupported DNS server IP address")
		}

		return nil
	}

	_, valid := networkDomain(host)
	if !valid {
		return errors.New("invalid DNS server domain name")
	}

	return nil
}

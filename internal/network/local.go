package network

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
)

type networkLocalAccess struct {
	mutex    sync.Mutex
	snapshot atomic.Pointer[networkLocalSnapshot]
	prefixes []netip.Prefix
}

type networkLocalSnapshot struct {
	addresses map[netip.Addr]struct{}
	prefixes  []netip.Prefix
}

var networkSharedLAN = netip.MustParsePrefix("100.64.0.0/10")

func (policy *networkPolicy) permitsLocal(address netip.Addr) bool {
	if policy.allowLoopback && policy.allowLAN {
		return true
	}

	if address.IsLoopback() {
		return policy.allowLoopback
	}

	local := policy.local.snapshot.Load()
	if local == nil {
		return false
	}

	if _, exists := local.addresses[address]; exists {
		return policy.allowLoopback
	}

	if policy.allowLAN {
		return true
	}

	if address.IsPrivate() || address.IsLinkLocalUnicast() || networkSharedLAN.Contains(address) {
		return false
	}

	for _, prefix := range local.prefixes {
		if prefix.Contains(address) {
			return false
		}
	}

	return true
}

func (policy *networkPolicy) checkDestination(address netip.Addr) error {
	if !networkDestination(address) || policy.mode == "closed" {
		return errNetworkDenied
	}

	err := policy.refreshLocal()
	if err != nil || !policy.permits(address) {
		return errNetworkDenied
	}

	return nil
}

func (policy *networkPolicy) refreshLocal() error {
	if policy.allowLoopback && policy.allowLAN {
		return nil
	}

	policy.local.mutex.Lock()
	defer policy.local.mutex.Unlock()

	addresses, err := net.InterfaceAddrs()
	if err != nil {
		policy.local.snapshot.Store(nil)

		return err
	}

	return policy.local.update(addresses)
}

// update requires mutex. Scratch prefixes are reused, but published snapshots
// remain immutable for packet-policy readers.
func (local *networkLocalAccess) update(addresses []net.Addr) error {
	if len(addresses) > 4096 {
		local.snapshot.Store(nil)

		return errors.New("too many host interface addresses")
	}

	if cap(local.prefixes) < len(addresses) {
		local.prefixes = make([]netip.Prefix, 0, len(addresses))
	}

	local.prefixes = local.prefixes[:0]

	for _, address := range addresses {
		prefix, parseErr := networkInterfacePrefix(address)
		if parseErr != nil {
			local.snapshot.Store(nil)

			return parseErr
		}

		local.prefixes = append(local.prefixes, prefix)
	}

	previous := local.snapshot.Load()
	if previous != nil && slices.Equal(previous.prefixes, local.prefixes) {
		return nil
	}

	snapshot := &networkLocalSnapshot{
		addresses: make(map[netip.Addr]struct{}, len(addresses)),
		prefixes:  slices.Clone(local.prefixes),
	}

	for _, prefix := range snapshot.prefixes {
		snapshot.addresses[prefix.Addr()] = struct{}{}
	}

	local.snapshot.Store(snapshot)

	return nil
}

func networkInterfacePrefix(address net.Addr) (netip.Prefix, error) {
	network, ok := address.(*net.IPNet)
	if !ok || network == nil {
		return netip.Prefix{}, errors.New("invalid host interface address")
	}

	ip, ok := netip.AddrFromSlice(network.IP)
	if !ok {
		return netip.Prefix{}, errors.New("invalid host interface address")
	}

	ip = ip.Unmap()
	mask := network.Mask

	if ip.Is4() && len(mask) == net.IPv6len {
		mask = mask[12:]
	}

	ones, bits := mask.Size()
	if bits != ip.BitLen() {
		return netip.Prefix{}, errors.New("invalid host interface mask")
	}

	return netip.PrefixFrom(ip, ones), nil
}

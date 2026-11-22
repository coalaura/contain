package network

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
)

type networkLocalAccess struct {
	mutex    sync.Mutex
	snapshot atomic.Pointer[networkLocalSnapshot]
}

type networkLocalSnapshot struct {
	addresses map[netip.Addr]struct{}
	subnets   []netip.Prefix
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

	for _, subnet := range local.subnets {
		if subnet.Contains(address) {
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

	if len(addresses) > 4096 {
		policy.local.snapshot.Store(nil)

		return errors.New("too many host interface addresses")
	}

	snapshot := &networkLocalSnapshot{
		addresses: make(map[netip.Addr]struct{}, len(addresses)),
		subnets:   make([]netip.Prefix, 0, len(addresses)),
	}

	for _, address := range addresses {
		prefix, parseErr := netip.ParsePrefix(address.String())
		if parseErr != nil {
			policy.local.snapshot.Store(nil)

			return errors.New("invalid host interface address")
		}

		snapshot.addresses[prefix.Addr().Unmap()] = struct{}{}
		snapshot.subnets = append(snapshot.subnets, prefix.Masked())
	}

	policy.local.snapshot.Store(snapshot)

	return nil
}

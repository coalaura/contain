package network

import (
	"container/list"
	"encoding/binary"
	"net/netip"
	"sync"
	"time"
)

const (
	dnsCacheEntries = 1024
	dnsCacheBytes   = 8 * 1024 * 1024
	dnsCacheSweep   = time.Minute
	dnsAddressLimit = 64
)

type dnsCache struct {
	mutex       sync.Mutex
	entries     map[string]*list.Element
	recent      list.List
	bytes       int
	nextSweep   time.Time
	grantsMutex sync.RWMutex
	grants      map[netip.Addr]map[*dnsCacheEntry]struct{}
}

type dnsCacheEntry struct {
	key       string
	wire      []byte
	ttls      []int
	addresses []netip.Addr
	stored    time.Time
	expires   time.Time
}

func (cache *dnsCache) permits(address netip.Addr) bool {
	// Packet authorization does not need the LRU lock or change recency.
	cache.grantsMutex.RLock()
	defer cache.grantsMutex.RUnlock()

	now := time.Now()

	for entry := range cache.grants[address] {
		if now.Before(entry.expires) {
			return true
		}
	}

	return false
}

func (cache *dnsCache) lookup(key string, identifier uint16) []byte {
	cache.mutex.Lock()

	element := cache.entries[key]
	if element == nil {
		cache.mutex.Unlock()

		return nil
	}

	entry := element.Value.(*dnsCacheEntry)
	now := time.Now()

	if !now.Before(entry.expires) {
		cache.grantsMutex.Lock()
		cache.remove(element)

		cache.grantsMutex.Unlock()
		cache.mutex.Unlock()

		return nil
	}

	cache.recent.MoveToFront(element)
	cache.mutex.Unlock()

	return entry.reply(identifier, now)
}

func (entry *dnsCacheEntry) reply(identifier uint16, now time.Time) []byte {
	// Entries are immutable after publication, so eviction cannot race with copying.
	wire := append([]byte(nil), entry.wire...)
	binary.BigEndian.PutUint16(wire, identifier)

	// Round age up so clients are never told to cache beyond the host grant.
	age := uint32((now.Sub(entry.stored) + time.Second - 1) / time.Second)

	for _, offset := range entry.ttls {
		ttl := binary.BigEndian.Uint32(wire[offset:])
		binary.BigEndian.PutUint32(wire[offset:], ttl-min(ttl, age))
	}

	return wire
}

func (cache *dnsCache) store(entry *dnsCacheEntry) {
	size := len(entry.key) + len(entry.wire)
	if size > dnsCacheBytes {
		return
	}

	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	// Writers always take the LRU lock before the grants lock.
	cache.grantsMutex.Lock()
	defer cache.grantsMutex.Unlock()

	now := time.Now()
	if !now.Before(entry.expires) {
		return
	}

	previous := cache.entries[entry.key]
	if previous != nil {
		cache.remove(previous)
	}

	// Expiry is checked on reads; sweeping only reclaims space and need not run per insert.
	if !now.Before(cache.nextSweep) {
		for element := cache.recent.Back(); element != nil; {
			previous = element.Prev()

			cached := element.Value.(*dnsCacheEntry)
			if !now.Before(cached.expires) {
				cache.remove(element)
			}

			element = previous
		}

		cache.nextSweep = now.Add(dnsCacheSweep)
	}

	for len(cache.entries) >= dnsCacheEntries || cache.bytes+size > dnsCacheBytes {
		cache.remove(cache.recent.Back())
	}

	cache.entries[entry.key] = cache.recent.PushFront(entry)
	cache.bytes += size

	for _, address := range entry.addresses {
		grants := cache.grants[address]
		if grants == nil {
			grants = make(map[*dnsCacheEntry]struct{}, 1)
			cache.grants[address] = grants
		}

		grants[entry] = struct{}{}
	}
}

// remove requires both the LRU and grants locks.
func (cache *dnsCache) remove(element *list.Element) {
	entry := element.Value.(*dnsCacheEntry)
	delete(cache.entries, entry.key)
	cache.recent.Remove(element)
	cache.bytes -= len(entry.key) + len(entry.wire)

	for _, address := range entry.addresses {
		grants := cache.grants[address]
		delete(grants, entry)

		if len(grants) == 0 {
			delete(cache.grants, address)
		}
	}
}

func newDNSCache() *dnsCache {
	return &dnsCache{
		entries: make(map[string]*list.Element, dnsCacheEntries),
		grants:  make(map[netip.Addr]map[*dnsCacheEntry]struct{}, dnsCacheEntries),
	}
}

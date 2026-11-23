package network

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const dnsQueryConcurrency = 32

type dnsQueryFlight struct {
	done   chan struct{}
	result *dnsCacheEntry
	source string
}

func (gateway *Gateway) queryDNS(ctx context.Context, key string, wire []byte, query *dnsmessage.Message) ([]byte, string) {
	gateway.mutex.Lock()

	if gateway.closed || gateway.ctx.Err() != nil || ctx.Err() != nil {
		gateway.mutex.Unlock()

		return dnsReply(query, dnsmessage.RCodeServerFailure, false), "query canceled"
	}

	// A lookup may have finished between the first cache check and this lock.
	cached := gateway.policy.dns.lookup(key, query.ID)
	if cached != nil {
		gateway.mutex.Unlock()

		return cached, "cache"
	}

	flight := gateway.dnsQueries[key]
	if flight == nil {
		if len(gateway.dnsQueries) == dnsQueryConcurrency {
			gateway.mutex.Unlock()

			return dnsReply(query, dnsmessage.RCodeServerFailure, false), "query limit"
		}

		flight = &dnsQueryFlight{done: make(chan struct{})}
		gateway.dnsQueries[key] = flight

		// The caller can return on cancellation and reuse its input while the
		// bounded, gateway-owned lookup continues for other callers.
		request := bytes.Clone(wire)

		gateway.workers.Go(func() {
			gateway.runDNSQuery(key, request, flight)
		})
	}

	gateway.mutex.Unlock()

	select {
	case <-ctx.Done():
		return dnsReply(query, dnsmessage.RCodeServerFailure, false), "query canceled"
	case <-gateway.ctx.Done():
		return dnsReply(query, dnsmessage.RCodeServerFailure, false), "query canceled"
	case <-flight.done:
		if flight.result == nil {
			return dnsReply(query, dnsmessage.RCodeServerFailure, false), flight.source
		}

		return flight.result.reply(query.ID, time.Now()), flight.source
	}
}

func (gateway *Gateway) runDNSQuery(key string, wire []byte, flight *dnsQueryFlight) {
	query, err := parseDNSQuery(wire)
	if err != nil {
		flight.source = "invalid query"
	} else {
		flight.result, flight.source = gateway.lookupDNS(key, wire, query)
	}

	gateway.mutex.Lock()
	delete(gateway.dnsQueries, key)
	close(flight.done)
	gateway.mutex.Unlock()
}

func (gateway *Gateway) lookupDNS(key string, wire []byte, query *dnsmessage.Message) (*dnsCacheEntry, string) {
	upstreams := [2]string{gateway.dns.Primary, gateway.dns.Secondary}

	for _, upstream := range upstreams {
		if upstream == "" || gateway.ctx.Err() != nil {
			continue
		}

		response, message, err := gateway.exchangeDNS(gateway.ctx, upstream, wire, query)
		if err != nil {
			continue
		}

		var addresses []netip.Addr

		if gateway.policy.mode == "isolated" && message.RCode == dnsmessage.RCodeSuccess {
			addresses, err = gateway.policy.dnsAddresses(message)
			if errors.Is(err, errNetworkDenied) {
				return &dnsCacheEntry{wire: dnsReply(query, dnsmessage.RCodeRefused, false), stored: time.Now()}, "CNAME policy"
			}

			if err != nil {
				continue
			}
		}

		entry := dnsCacheRecord(key, response, message, addresses)
		if entry != nil {
			gateway.policy.dns.store(entry)
		} else {
			// Non-cacheable replies are still shared with current callers. Age
			// their TTLs too, including negative responses and zero-TTL answers.
			entry = &dnsCacheEntry{wire: response, ttls: dnsTTLOffsets(response, message), stored: time.Now()}
		}

		return entry, "upstream"
	}

	return nil, "upstream failed"
}

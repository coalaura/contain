package network

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	dnsQueryLimit    = 4096
	dnsResponseLimit = 65535
	dnsUDPLimit      = 1232
)

var errDNSMessage = errors.New("invalid or unsupported DNS message")

func (policy *networkPolicy) permitsDomain(name string) bool {
	if policy.mode == "closed" {
		return false
	}

	if policy.mode == "open" {
		return true
	}

	name = dnsDomain(name)
	if _, denied := policy.denyDomains[name]; denied {
		return false
	}

	_, allowed := policy.allowDomains[name]

	return allowed || policy.defaultAllow
}

func (policy *networkPolicy) dnsAddresses(message *dnsmessage.Message) ([]netip.Addr, error) {
	name := dnsDomain(message.Questions[0].Name.String())
	questionType := message.Questions[0].Type
	addresses := make([]netip.Addr, 0, min(len(message.Answers), dnsAddressLimit))

	// Follow only the queried name's CNAME chain, never unrelated additional records.
	for depth := 0; depth <= 16; depth++ {
		if policy.mode != "open" {
			if _, denied := policy.denyDomains[name]; denied {
				return nil, errNetworkDenied
			}
		}

		target := ""

		for index := range message.Answers {
			answer := &message.Answers[index]
			if answer.Header.Class != dnsmessage.ClassINET || dnsDomain(answer.Header.Name.String()) != name {
				continue
			}

			switch body := answer.Body.(type) {
			case *dnsmessage.CNAMEResource:
				candidate := dnsDomain(body.CNAME.String())
				if target != "" && target != candidate {
					return nil, errDNSMessage
				}

				target = candidate
			case *dnsmessage.AResource:
				if questionType == dnsmessage.TypeA || questionType == dnsmessage.TypeALL {
					addresses = append(addresses, netip.AddrFrom4(body.A))
				}
			case *dnsmessage.AAAAResource:
				if questionType == dnsmessage.TypeAAAA || questionType == dnsmessage.TypeALL {
					addresses = append(addresses, netip.AddrFrom16(body.AAAA).Unmap())
				}
			}

			if len(addresses) > dnsAddressLimit {
				return nil, errDNSMessage
			}
		}

		if target == "" {
			return addresses, nil
		}

		if len(addresses) != 0 {
			return nil, errDNSMessage
		}

		name = target
	}

	return nil, errDNSMessage
}

func parseDNSQuery(wire []byte) (*dnsmessage.Message, error) {
	if len(wire) < 12 || len(wire) > dnsQueryLimit {
		return nil, errDNSMessage
	}

	// Questions use uncompressed names. Reject literal dots inside labels so the
	// parser's dotted representation cannot match a different configured name.
	offset := 12

	for {
		if offset >= len(wire) {
			return nil, errDNSMessage
		}

		length := int(wire[offset])
		offset++

		if length == 0 {
			break
		}

		if length > 63 || offset+length > len(wire) {
			return nil, errDNSMessage
		}

		for _, character := range wire[offset : offset+length] {
			if character < 0x21 || character > 0x7e || character == '.' {
				return nil, errDNSMessage
			}
		}

		offset += length
	}

	var message dnsmessage.Message

	err := message.Unpack(wire)
	if err != nil || message.Response || message.OpCode != 0 || message.Truncated || message.RCode != 0 || len(message.Questions) != 1 || len(message.Answers) != 0 || len(message.Authorities) != 0 || len(message.Additionals) > 1 {
		return nil, errDNSMessage
	}

	if message.Questions[0].Class != dnsmessage.ClassINET {
		return nil, errDNSMessage
	}

	if len(message.Additionals) == 1 && message.Additionals[0].Header.Type != dnsmessage.TypeOPT {
		return nil, errDNSMessage
	}

	return &message, nil
}

func parseDNSResponse(wire []byte, query *dnsmessage.Message) (*dnsmessage.Message, error) {
	if len(wire) < 12 || len(wire) > dnsResponseLimit {
		return nil, errDNSMessage
	}

	var message dnsmessage.Message

	err := message.Unpack(wire)
	if err != nil || !message.Response || message.OpCode != 0 || message.ID != query.ID || len(message.Questions) != 1 {
		return nil, errDNSMessage
	}

	question := message.Questions[0]
	expected := query.Questions[0]

	if question.Type != expected.Type || question.Class != expected.Class || !strings.EqualFold(question.Name.String(), expected.Name.String()) {
		return nil, errDNSMessage
	}

	return &message, nil
}

func dnsReply(query *dnsmessage.Message, code dnsmessage.RCode, truncated bool) []byte {
	message := dnsmessage.Message{
		ID:                 query.ID,
		Response:           true,
		RecursionDesired:   query.RecursionDesired,
		RecursionAvailable: true,
		CheckingDisabled:   query.CheckingDisabled,
		RCode:              code,
		Truncated:          truncated,
		Questions:          query.Questions,
	}

	wire, _ := message.Pack()

	return wire
}

func dnsCacheRecord(key string, wire []byte, message *dnsmessage.Message, addresses []netip.Addr) *dnsCacheEntry {
	if message.Truncated || message.RCode != dnsmessage.RCodeSuccess || len(message.Answers) == 0 {
		return nil
	}

	ttls := dnsTTLOffsets(wire, message)
	if len(ttls) == 0 {
		return nil
	}

	lifetime := uint32(1<<31 - 1)

	for _, offset := range ttls {
		ttl := binary.BigEndian.Uint32(wire[offset:])
		if ttl == 0 || ttl > 1<<31-1 {
			return nil
		}

		lifetime = min(lifetime, ttl)
	}

	now := time.Now()

	return &dnsCacheEntry{
		key:       key,
		wire:      append([]byte(nil), wire...),
		ttls:      ttls,
		addresses: addresses,
		stored:    now,
		expires:   now.Add(time.Duration(lifetime) * time.Second),
	}
}

func dnsTTLOffsets(wire []byte, message *dnsmessage.Message) []int {
	offset := dnsSkipName(wire, 12)
	if offset < 0 || offset+4 > len(wire) {
		return nil
	}

	offset += 4

	count := len(message.Answers) + len(message.Authorities) + len(message.Additionals)
	ttls := make([]int, 0, count)

	// Keep the original wire image: unknown RDATA can contain compression pointers.
	for range count {
		offset = dnsSkipName(wire, offset)
		if offset < 0 || offset+10 > len(wire) {
			return nil
		}

		kind := dnsmessage.Type(binary.BigEndian.Uint16(wire[offset:]))
		if kind != dnsmessage.TypeOPT {
			ttls = append(ttls, offset+4)
		}

		length := int(binary.BigEndian.Uint16(wire[offset+8:]))

		offset += 10 + length
		if offset > len(wire) {
			return nil
		}
	}

	if offset != len(wire) {
		return nil
	}

	return ttls
}

func dnsSkipName(wire []byte, offset int) int {
	for offset < len(wire) {
		length := int(wire[offset])
		offset++

		if length == 0 {
			return offset
		}

		if length&0xc0 == 0xc0 && offset < len(wire) {
			return offset + 1
		}

		if length > 63 {
			return -1
		}

		offset += length
	}

	return -1
}

func dnsDomain(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

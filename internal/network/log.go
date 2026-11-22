package network

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const networkLogRate = 100

type networkEvent struct {
	protocol    string
	action      string
	target      string
	destination netip.AddrPort
	reason      string
	method      string
	queryType   string
	result      string
	status      int
	started     time.Time
}

type networkLogger struct {
	mutex      sync.Mutex
	output     io.Writer
	buffer     []byte
	window     time.Time
	count      int
	suppressed uint64
}

func (logger *networkLogger) record(event networkEvent) {
	if logger == nil {
		return
	}

	logger.mutex.Lock()
	defer logger.mutex.Unlock()

	now := time.Now()
	if now.Sub(logger.window) >= time.Second {
		logger.flushSuppressed(now)

		logger.window = now
		logger.count = 0
	}

	if logger.count == networkLogRate {
		logger.suppressed++

		return
	}

	logger.count++

	logger.buffer = now.UTC().AppendFormat(logger.buffer[:0], "2006-01-02T15:04:05.000Z")
	logger.buffer = append(logger.buffer, ' ')
	logger.buffer = append(logger.buffer, event.protocol...)
	logger.buffer = append(logger.buffer, ' ')
	logger.buffer = append(logger.buffer, event.action...)

	logger.field("target", event.target)

	if event.destination.IsValid() {
		logger.buffer = append(logger.buffer, " dst="...)

		if event.destination.Port() == 0 {
			logger.buffer = event.destination.Addr().AppendTo(logger.buffer)
		} else {
			logger.buffer = event.destination.AppendTo(logger.buffer)
		}
	}

	logger.field("method", event.method)
	logger.field("type", event.queryType)
	logger.field("result", event.result)
	logger.field("reason", event.reason)

	if event.status != 0 {
		logger.buffer = append(logger.buffer, " status="...)
		logger.buffer = strconv.AppendInt(logger.buffer, int64(event.status), 10)
	}

	if !event.started.IsZero() {
		logger.buffer = append(logger.buffer, " duration_ms="...)
		logger.buffer = strconv.AppendInt(logger.buffer, now.Sub(event.started).Milliseconds(), 10)
	}

	logger.buffer = append(logger.buffer, '\n')
	logger.output.Write(logger.buffer)
}

func (logger *networkLogger) field(name, value string) {
	if value == "" {
		return
	}

	// Bound and quote peer-controlled names so they cannot forge lines or terminal escapes.
	if len(value) > 253 {
		value = value[:253]
	}

	logger.buffer = append(logger.buffer, ' ')
	logger.buffer = append(logger.buffer, name...)
	logger.buffer = append(logger.buffer, '=')
	logger.buffer = strconv.AppendQuoteToASCII(logger.buffer, value)
}

func (logger *networkLogger) flush() {
	if logger == nil {
		return
	}

	logger.mutex.Lock()
	defer logger.mutex.Unlock()

	logger.flushSuppressed(time.Now())
}

func (logger *networkLogger) flushSuppressed(now time.Time) {
	if logger.suppressed == 0 {
		return
	}

	logger.buffer = now.UTC().AppendFormat(logger.buffer[:0], "2006-01-02T15:04:05.000Z")
	logger.buffer = append(logger.buffer, " LOG SUPPRESSED count="...)
	logger.buffer = strconv.AppendUint(logger.buffer, logger.suppressed, 10)
	logger.buffer = append(logger.buffer, '\n')

	logger.output.Write(logger.buffer)
	logger.suppressed = 0
}

func (gateway *Gateway) logConnection(protocol string, destination netip.AddrPort, err error) {
	action, reason := networkLogResult(err)
	gateway.logger.record(networkEvent{protocol: protocol, action: action, destination: destination, reason: reason})
}

func (gateway *Gateway) logDNS(query *dnsmessage.Message, response []byte, started time.Time, source string) {
	if gateway.logger == nil || len(response) < 4 {
		return
	}

	code := dnsmessage.RCode(response[3] & 15)
	action := "REPLY"

	switch code {
	case dnsmessage.RCodeRefused:
		action = "DENY"
	case dnsmessage.RCodeServerFailure:
		action = "ERROR"
	}

	gateway.logger.record(networkEvent{
		protocol:  "DNS",
		action:    action,
		target:    strings.TrimSuffix(query.Questions[0].Name.String(), "."),
		queryType: strings.TrimPrefix(query.Questions[0].Type.String(), "Type"),
		result:    strings.TrimPrefix(code.String(), "RCode"),
		reason:    source,
		started:   started,
	})
}

func (gateway *Gateway) logPacketDrop(parsed networkPacket, err error) {
	if !errors.Is(err, errNetworkDenied) {
		gateway.logger.record(networkEvent{protocol: "PACKET", action: "DROP", reason: "malformed or unsupported"})

		return
	}

	protocol := "ICMP"

	switch parsed.protocol {
	case 6:
		protocol = "TCP"
	case 17:
		protocol = "UDP"
	}

	gateway.logConnection(protocol, netip.AddrPortFrom(parsed.destination, parsed.port), err)
}

func newNetworkLogger(output io.Writer) *networkLogger {
	if output == nil {
		return nil
	}

	return &networkLogger{output: output, buffer: make([]byte, 0, 4096)}
}

func networkLogResult(err error) (string, string) {
	switch {
	case err == nil:
		return "ALLOW", ""
	case errors.Is(err, errNetworkDenied):
		return "DENY", "network policy"
	case errors.Is(err, context.Canceled):
		return "CANCEL", "session canceled"
	}

	networkError, ok := errors.AsType[net.Error](err)
	if ok && networkError.Timeout() {
		return "ERROR", "timeout"
	}

	// Raw errors may contain credentials, URLs or upstream-controlled text.
	return "ERROR", "connection failed"
}

func networkLogMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return method
	default:
		return "OTHER"
	}
}

package network

import (
	"container/list"
	"crypto/tls"
	"errors"
	"time"
)

const networkCertificates = 256

type networkCertificate struct {
	identity    string
	certificate *tls.Certificate
	expires     time.Time
}

func (authority *Authority) issue(identity string) (*tls.Certificate, error) {
	// Signing under the lock coalesces concurrent misses without an unbounded
	// in-flight map. Cached certificates are immutable after publication.
	authority.mutex.Lock()
	defer authority.mutex.Unlock()

	now := time.Now()
	if now.Before(authority.certificate.NotBefore) || !now.Before(authority.certificate.NotAfter) {
		return nil, errors.New("contain network CA is not currently valid")
	}

	element := authority.certificates[identity]
	if element != nil {
		cached := element.Value.(*networkCertificate)
		if now.Before(cached.expires) {
			authority.order.MoveToFront(element)

			return cached.certificate, nil
		}

		delete(authority.certificates, identity)
		authority.order.Remove(element)
	}

	expires := now.Add(24 * time.Hour)
	if authority.certificate.NotAfter.Before(expires) {
		expires = authority.certificate.NotAfter
	}

	// X.509 validity is encoded with second precision.
	expires = expires.Truncate(time.Second)
	if !now.Before(expires) {
		return nil, errors.New("contain network CA expires too soon")
	}

	certificate, err := authority.sign(identity, expires)
	if err != nil {
		return nil, err
	}

	if authority.certificates == nil {
		authority.certificates = make(map[string]*list.Element, networkCertificates)
	}

	if len(authority.certificates) == networkCertificates {
		oldest := authority.order.Back()
		cached := oldest.Value.(*networkCertificate)

		delete(authority.certificates, cached.identity)
		authority.order.Remove(oldest)
	}

	cached := &networkCertificate{identity: identity, certificate: certificate, expires: expires}
	authority.certificates[identity] = authority.order.PushFront(cached)

	return certificate, nil
}

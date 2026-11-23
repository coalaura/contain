package network

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Authority struct {
	certificate  *x509.Certificate
	key          *ecdsa.PrivateKey
	public       []byte
	mutex        sync.Mutex
	certificates map[string]*list.Element
	order        list.List
}

func (authority *Authority) PublicCertificate() []byte {
	return authority.public
}

func (authority *Authority) sign(identity string, expires time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	certificate := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: identity},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     expires,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	address, err := netip.ParseAddr(identity)
	if err == nil {
		certificate.IPAddresses = append(certificate.IPAddresses, address.AsSlice())
	} else {
		certificate.DNSNames = []string{identity}
	}

	der, err := x509.CreateCertificate(rand.Reader, certificate, authority.certificate, &key.PublicKey, authority.key)
	if err != nil {
		return nil, err
	}

	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func LoadAuthority(imageDirectory string) (*Authority, error) {
	directory := filepath.Join(imageDirectory, "network", "ca")

	err := os.MkdirAll(directory, 0700)
	if err != nil {
		return nil, err
	}

	err = protectNetworkDirectory(directory)
	if err != nil {
		return nil, err
	}

	filename := filepath.Join(directory, "authority.pem")

	data, err := readNetworkFile(filename, 32768)
	if errors.Is(err, os.ErrNotExist) {
		data, err = createNetworkCA(filename)
	}

	if err != nil {
		return nil, err
	}

	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		return nil, errors.New("invalid contain network CA")
	}

	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !certificate.IsCA || time.Now().After(certificate.NotAfter) {
		return nil, errors.New("contain network CA is invalid or expired")
	}

	key, valid := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !valid {
		return nil, errors.New("invalid contain network CA signing key")
	}

	public := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})

	return &Authority{certificate: certificate, key: key, public: public}, nil
}

func createNetworkCA(filename string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	certificate := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "contain private network CA"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}

	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})...)

	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return readNetworkFile(filename, 32768)
	}

	if err != nil {
		return nil, err
	}

	_, err = file.Write(data)
	closeErr := file.Close()

	if err != nil {
		return nil, err
	}

	return data, closeErr
}

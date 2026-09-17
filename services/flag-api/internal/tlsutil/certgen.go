package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// GenerateCACert creates a self-signed CA certificate for Tombstone internal PKI.
// Uses ECDSA P256 — lighter weight than RSA and sufficient for internal service mesh.
func GenerateCACert() (*tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tombstone-internal-ca", Organization: []string{"Tombstone"}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	caCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	tlsCert := tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key, Leaf: caCert}
	return &tlsCert, pool, nil
}

// GenerateServiceCert creates a TLS certificate signed by the CA for a named service.
// The CN follows the pattern tombstone.svc.<serviceName> for service identity.
func GenerateServiceCert(caTLSCert *tls.Certificate, serviceName string) (*tls.Certificate, error) {
	caX509, err := x509.ParseCertificate(caTLSCert.Certificate[0])
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "tombstone.svc." + serviceName},
		DNSNames:     []string{serviceName, "tombstone.svc." + serviceName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, caX509, &key.PublicKey, caTLSCert.PrivateKey)
	if err != nil {
		return nil, err
	}
	tlsCert := &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key}
	return tlsCert, nil
}

// WriteCerts writes PEM-encoded cert files to certsDir.
// Creates: ca.crt, flag-api.crt, flag-api.key, client.crt, client.key
func WriteCerts(certsDir string, caCert *tls.Certificate, serverCert *tls.Certificate, clientCert *tls.Certificate) error {
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return err
	}
	write := func(name string, cert *tls.Certificate, isCA bool) error {
		certOut, err := os.Create(filepath.Join(certsDir, name+".crt"))
		if err != nil {
			return err
		}
		defer func() { _ = certOut.Close() }()
		if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}); err != nil {
			return err
		}
		if isCA {
			return nil
		}
		keyOut, err := os.OpenFile(filepath.Join(certsDir, name+".key"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		defer func() { _ = keyOut.Close() }()
		keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
		if err != nil {
			return err
		}
		return pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	}
	if err := write("ca", caCert, true); err != nil {
		return err
	}
	if err := write("flag-api", serverCert, false); err != nil {
		return err
	}
	return write("client", clientCert, false)
}

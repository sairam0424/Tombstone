package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
)

// LoadServerTLSConfig loads the flag-api server TLS config requiring mTLS client certs.
// Expects: flag-api.crt, flag-api.key, ca.crt in certsDir.
func LoadServerTLSConfig(certsDir string) (*tls.Config, error) {
	serverCert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "flag-api.crt"),
		filepath.Join(certsDir, "flag-api.key"),
	)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// LoadClientTLSConfig loads the mTLS client config for services calling flag-api.
// Expects: client.crt, client.key, ca.crt in certsDir.
func LoadClientTLSConfig(certsDir string) (*tls.Config, error) {
	clientCert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "client.crt"),
		filepath.Join(certsDir, "client.key"),
	)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

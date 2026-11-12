package apiserver

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// validateEtcdTLS refuses a partial set of the etcd TLS files and names each
// flag that is missing.
func (c Config) validateEtcdTLS() error {
	var missing []string
	for _, f := range []struct{ flag, value string }{
		{"etcd-ca-file", c.EtcdCA},
		{"etcd-cert-file", c.EtcdCert},
		{"etcd-key-file", c.EtcdKey},
	} {
		if f.value == "" {
			missing = append(missing, f.flag)
		}
	}
	if len(missing) == 0 || len(missing) == 3 {
		return nil
	}
	return fmt.Errorf("etcd-ca-file, etcd-cert-file and etcd-key-file are given all together or not at all: missing %s", strings.Join(missing, ", "))
}

// etcdTLS returns the TLS configuration that verifies etcd against the etcd CA
// and presents the client certificate, or nil where c names no etcd TLS files.
func (c Config) etcdTLS() (*tls.Config, error) {
	if err := c.validateEtcdTLS(); err != nil || c.EtcdCA == "" {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.EtcdCert, c.EtcdKey)
	if err != nil {
		return nil, fmt.Errorf("load etcd client certificate: %w", err)
	}
	pem, err := os.ReadFile(c.EtcdCA)
	if err != nil {
		return nil, fmt.Errorf("read etcd CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("etcd CA %s holds no certificate", c.EtcdCA)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}, nil
}

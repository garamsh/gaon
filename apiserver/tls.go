package apiserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// tlsGiven reports whether c names the TLS files, and refuses a partial set.
func (c Config) tlsGiven() (bool, error) {
	n := 0
	for _, f := range []string{c.TLSClientCA, c.TLSCert, c.TLSKey} {
		if f != "" {
			n++
		}
	}
	if n != 0 && n != 3 {
		return false, errors.New("tls-client-ca, tls-cert and tls-key are given all together or not at all")
	}
	return n == 3, nil
}

// serverTLS returns the TLS configuration that requires a client certificate
// the client CA signed, or nil where c names no TLS files.
func (c Config) serverTLS() (*tls.Config, error) {
	given, err := c.tlsGiven()
	if err != nil || !given {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	pem, err := os.ReadFile(c.TLSClientCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client CA %s holds no certificate", c.TLSClientCA)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

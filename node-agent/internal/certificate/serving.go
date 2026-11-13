package certificate

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"
)

// ServerCertificate is the tls.Config.GetCertificate of the agent's listener.
// It presents the current pair, and fails while there is none or while its
// certificate lacks server auth, as one issued before the agent asked for it.
func (h *Holder) ServerCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	pair := h.get()
	if pair == nil {
		return nil, errors.New("the agent holds no certificate yet")
	}
	if !hasUsage(pair.Leaf, x509.ExtKeyUsageServerAuth) {
		return nil, errors.New("the agent's certificate lacks server auth: it renews into one that has it")
	}
	return pair, nil
}

// ServingPair reads the certificate and key of an operator and returns the
// pair when the agent can serve with it: it chains to the CAs in caFile, is
// for node and carries both server auth and client auth.
func ServingPair(certFile, keyFile, caFile, node string, now time.Time) (*tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("certificate and key: %w", err)
	}
	pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if err := checkLeaf(pair.Leaf, node, now); err != nil {
		return nil, err
	}
	for _, u := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if !hasUsage(pair.Leaf, u) {
			return nil, errors.New("certificate lacks server auth or client auth: it needs both")
		}
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tls ca: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("tls ca %s: no certificate found", caFile)
	}
	inter := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		if c, err := x509.ParseCertificate(der); err == nil {
			inter.AddCert(c)
		}
	}
	_, err = pair.Leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inter, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return nil, fmt.Errorf("certificate does not chain to the cluster CA: %w", err)
	}
	return &pair, nil
}

// hasUsage reports whether leaf states usage in its extended key usage.
func hasUsage(leaf *x509.Certificate, usage x509.ExtKeyUsage) bool {
	return leaf != nil && slices.Contains(leaf.ExtKeyUsage, usage)
}

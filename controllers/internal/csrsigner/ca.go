package csrsigner

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// CA is the cluster CA: the certificate and the key that signs with it.
type CA struct {
	cert *x509.Certificate
	key  crypto.Signer
}

// LoadCA reads the PEM files of the CA certificate and key. It fails when a
// file does not load or the key does not belong to the certificate.
func LoadCA(certFile, keyFile string) (*CA, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("signing cert %s with key %s: %w", certFile, keyFile, err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("signing cert %s: %w", certFile, err)
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("signing key %s cannot sign", keyFile)
	}
	return &CA{cert: cert, key: key}, nil
}

package csrsigner

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
)

// sign issues the certificate for csr: its subject and public key, valid from
// five minutes before now for the configured duration, and never past the end
// of the CA certificate. It returns the certificate in PEM.
func (s *service) sign(csr *x509.CertificateRequest) (string, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", fmt.Errorf("serial number: %w", err)
	}
	now := s.cfg.Now()
	notAfter := now.Add(s.cfg.Duration)
	if caEnd := s.cfg.CA.cert.NotAfter; caEnd.Before(notAfter) {
		notAfter = caEnd
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		RawSubject:   csr.RawSubject,
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.cfg.CA.cert, csr.PublicKey, s.cfg.CA.key)
	if err != nil {
		return "", fmt.Errorf("sign certificate: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

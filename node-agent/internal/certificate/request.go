package certificate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// obtain creates a request for a certificate with a new key, waits for the
// signer's decision and returns the key and the certificate, both PEM.
func (s *service) obtain(ctx context.Context, reqs Requests) (keyPEM, certPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: s.params.NodeName, Organization: []string{api.NodeGroup}},
	}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate request: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return nil, nil, fmt.Errorf("name the request: %w", err)
	}
	name := s.params.NodeName + "-" + hex.EncodeToString(suffix)
	_, err = reqs.PutCertificateSigningRequest(ctx, &api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.CertificateSigningRequestSpec{Request: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create request %s: %w", name, err)
	}
	for {
		got, err := reqs.GetCertificateSigningRequest(ctx, name)
		if err != nil {
			return nil, nil, fmt.Errorf("read request %s: %w", name, err)
		}
		if reason, denied := denial(got); denied {
			return nil, nil, fmt.Errorf("request %s denied: %s", name, reason)
		}
		if approved(got) && got.Status.Certificate != "" {
			return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), []byte(got.Status.Certificate), nil
		}
		if err := s.clock.Sleep(ctx, pollPeriod); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("request %s has no decision", name), err)
		}
	}
}

func approved(r *api.CertificateSigningRequest) bool {
	for _, c := range r.Status.Conditions {
		if c.Type == api.CSRApproved && c.Status == api.ConditionTrue {
			return true
		}
	}
	return false
}

// denial returns the reason of the Denied condition of r, when it is true.
func denial(r *api.CertificateSigningRequest) (string, bool) {
	for _, c := range r.Status.Conditions {
		if c.Type == api.CSRDenied && c.Status == api.ConditionTrue {
			return strings.TrimSpace(c.Reason + " " + c.Message), true
		}
	}
	return "", false
}

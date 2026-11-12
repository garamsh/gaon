// Package rolesanywhere provides AWS credentials from IAM Roles Anywhere: it
// signs a CreateSession request with a certificate and its key, and trades
// the answer for temporary credentials. No long-lived AWS key is read.
package rolesanywhere

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	helper "github.com/aws/rolesanywhere-credential-helper/aws_signing_helper"
)

// CommonName is the CN the certificate that asks for credentials carries; the
// trust policy of the role names it.
const CommonName = "gaon-etcd-snapshot"

// SessionDuration is how long the credentials a session holds are valid.
const SessionDuration = time.Hour

const (
	algorithmECDSA = "AWS4-X509-ECDSA-SHA256"
	algorithmRSA   = "AWS4-X509-RSA-SHA256"
)

// Config says which session to ask for.
type Config struct {
	// CertFile and KeyFile are the paths of the PEM certificate, with any
	// intermediates after it, and of its key.
	CertFile, KeyFile string
	// TrustAnchorARN, ProfileARN and RoleARN name what the session is asked
	// under.
	TrustAnchorARN, ProfileARN, RoleARN string
	// Region is the region of the Roles Anywhere service.
	Region string
	// Endpoint, where not empty, replaces the Roles Anywhere endpoint.
	Endpoint string
}

// Validate names the first value a Config lacks.
func (c Config) Validate() error {
	for _, f := range []struct{ flag, value string }{
		{"rolesanywhere-cert-file", c.CertFile},
		{"rolesanywhere-key-file", c.KeyFile},
		{"rolesanywhere-trust-anchor-arn", c.TrustAnchorARN},
		{"rolesanywhere-profile-arn", c.ProfileARN},
		{"rolesanywhere-role-arn", c.RoleARN},
		{"region", c.Region},
	} {
		if f.value == "" {
			return fmt.Errorf("no %s given", f.flag)
		}
	}
	return nil
}

// New loads the certificate and key once, refuses a certificate whose CN is
// not CommonName or a key that does not belong to it, and returns the
// provider that asks for a session whenever the credentials it cached expire.
func New(cfg Config) (aws.CredentialsProvider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s, algorithm, err := loadSigner(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	opts := helper.CredentialsOpts{
		RoleArn:           cfg.RoleARN,
		ProfileArnStr:     cfg.ProfileARN,
		TrustAnchorArnStr: cfg.TrustAnchorARN,
		SessionDuration:   int(SessionDuration / time.Second),
		Region:            cfg.Region,
		Endpoint:          cfg.Endpoint,
		Version:           "gaon-etcd-snapshot",
	}
	return aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		o := opts
		out, err := helper.GenerateCredentials(&o, s, algorithm)
		if err != nil {
			return aws.Credentials{}, fmt.Errorf("roles anywhere session: %w", err)
		}
		expires, err := time.Parse(time.RFC3339, out.Expiration)
		if err != nil {
			return aws.Credentials{}, fmt.Errorf("roles anywhere session expiry: %w", err)
		}
		return aws.Credentials{
			AccessKeyID:     out.AccessKeyId,
			SecretAccessKey: out.SecretAccessKey,
			SessionToken:    out.SessionToken,
			Source:          "RolesAnywhere",
			CanExpire:       true,
			Expires:         expires,
		}, nil
	})), nil
}

// signer is the helper's Signer over a certificate and key held in memory. The
// helper's own file signer exits the process on a bad file and reads it again
// on every call.
type signer struct {
	key   crypto.Signer
	cert  *x509.Certificate
	chain []*x509.Certificate
}

func loadSigner(certFile, keyFile string) (*signer, string, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, "", fmt.Errorf("load roles anywhere certificate: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, "", errors.New("roles anywhere certificate file holds no certificate")
	}
	var parsed []*x509.Certificate
	for _, der := range pair.Certificate {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, "", fmt.Errorf("parse roles anywhere certificate: %w", err)
		}
		parsed = append(parsed, c)
	}
	if cn := parsed[0].Subject.CommonName; cn != CommonName {
		return nil, "", fmt.Errorf("roles anywhere certificate has CN %q, want %q", cn, CommonName)
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, "", errors.New("roles anywhere key cannot sign")
	}
	var algorithm string
	switch key.(type) {
	case *ecdsa.PrivateKey:
		algorithm = algorithmECDSA
	case *rsa.PrivateKey:
		algorithm = algorithmRSA
	default:
		return nil, "", fmt.Errorf("roles anywhere key type %T is neither ECDSA nor RSA", key)
	}
	return &signer{key: key, cert: parsed[0], chain: parsed[1:]}, algorithm, nil
}

func (s *signer) Public() crypto.PublicKey { return s.key.Public() }

func (s *signer) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return s.key.Sign(rand, digest, opts)
}

func (s *signer) Certificate() (*x509.Certificate, error) { return s.cert, nil }

func (s *signer) CertificateChain() ([]*x509.Certificate, error) { return s.chain, nil }

func (s *signer) Close() {}

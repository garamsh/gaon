// Package token signs and verifies service account tokens: JWTs of the claims
// api.NewTokenClaims builds, signed with ES256 by one configured key.
package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// Algorithm is the "alg" header of every token: ECDSA over P-256 and SHA-256.
const Algorithm = "ES256"

// Signer signs the claims of a token with a key it holds. It is safe for
// concurrent use.
type Signer struct {
	key    *ecdsa.PrivateKey
	issuer string
	kid    string
}

// NewSigner returns the Signer of issuer that signs with key, which must be on
// P-256.
func NewSigner(issuer string, key *ecdsa.PrivateKey) (*Signer, error) {
	if issuer == "" {
		return nil, errors.New("the token issuer is empty")
	}
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("the token signing key is on curve %s, want P-256", key.Curve.Params().Name)
	}
	kid, err := keyID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Signer{key: key, issuer: issuer, kid: kid}, nil
}

// keyID is the "kid" of a key: the SHA-256 of its PKIX encoding, so that every
// API server holding the key names it alike.
func keyID(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// LoadSigner reads the PEM private key in file, an EC key in SEC 1 or PKCS #8
// form, and returns the Signer of issuer for it. An error never holds key
// material.
func LoadSigner(issuer, file string) (*Signer, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read the token signing key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("the token signing key %s is not PEM", file)
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		if parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			var ok bool
			if key, ok = parsed.(*ecdsa.PrivateKey); !ok {
				return nil, fmt.Errorf("the token signing key %s is not an EC key", file)
			}
		}
	default:
		return nil, fmt.Errorf("the token signing key %s is a PEM %q, want EC PRIVATE KEY or PRIVATE KEY", file, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("the token signing key %s does not parse", file)
	}
	return NewSigner(issuer, key)
}

// Issuer is the "iss" of the tokens s signs.
func (s *Signer) Issuer() string { return s.issuer }

// Public is the verification key of the tokens s signs.
func (s *Signer) Public() *ecdsa.PublicKey { return &s.key.PublicKey }

// Sign returns the compact JWT of c.
func (s *Signer) Sign(c *api.TokenClaims) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": Algorithm, "typ": "JWT", "kid": s.kid})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// Verify checks that token is signed with the private half of pub and returns
// its claims. It checks the signature and the shape of the claims, not the
// time.
func Verify(token string, pub *ecdsa.PublicKey) (*api.TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("the token is not a compact JWT")
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("the token signature is malformed")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("the token signature does not verify")
	}
	payload, err := enc.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("the token payload is malformed")
	}
	var c api.TokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("the token claims are malformed")
	}
	return &c, c.Validate()
}

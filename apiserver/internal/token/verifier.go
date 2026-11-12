package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// Verifier checks the tokens of one issuer against the public keys it holds
// and publishes those keys as a JWKS. It is safe for concurrent use.
type Verifier struct {
	issuer string
	keys   map[string]*ecdsa.PublicKey
	jwks   []byte
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// NewVerifier returns the Verifier of issuer for keys, each on P-256. The
// first key is the one that signs; the others are kept for a rotation.
func NewVerifier(issuer string, keys ...*ecdsa.PublicKey) (*Verifier, error) {
	if issuer == "" {
		return nil, errors.New("the token issuer is empty")
	}
	if len(keys) == 0 {
		return nil, errors.New("a verifier needs a key")
	}
	v := &Verifier{issuer: issuer, keys: map[string]*ecdsa.PublicKey{}}
	set := struct {
		Keys []jwk `json:"keys"`
	}{Keys: []jwk{}}
	for _, pub := range keys {
		if pub.Curve != elliptic.P256() {
			return nil, fmt.Errorf("a token verification key is on curve %s, want P-256", pub.Curve.Params().Name)
		}
		kid, err := keyID(pub)
		if err != nil {
			return nil, err
		}
		if _, dup := v.keys[kid]; dup {
			continue
		}
		v.keys[kid] = pub
		point, err := pub.Bytes()
		if err != nil {
			return nil, err
		}
		enc := base64.RawURLEncoding
		set.Keys = append(set.Keys, jwk{Kty: "EC", Crv: "P-256", Use: "sig", Alg: Algorithm, Kid: kid, X: enc.EncodeToString(point[1:33]), Y: enc.EncodeToString(point[33:])})
	}
	var err error
	if v.jwks, err = json.Marshal(set); err != nil {
		return nil, err
	}
	return v, nil
}

// LoadPublicKeys reads the PEM public keys in files, each a PKIX "PUBLIC KEY"
// block on an EC curve, and returns them in order.
func LoadPublicKeys(files []string) ([]*ecdsa.PublicKey, error) {
	var keys []*ecdsa.PublicKey
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read the token verification key: %w", err)
		}
		found := 0
		for block, rest := pem.Decode(raw); block != nil; block, rest = pem.Decode(rest) {
			if block.Type != "PUBLIC KEY" {
				return nil, fmt.Errorf("the token verification key file %s holds a PEM %q, want PUBLIC KEY", file, block.Type)
			}
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("the token verification key file %s does not parse", file)
			}
			pub, ok := parsed.(*ecdsa.PublicKey)
			if !ok {
				return nil, fmt.Errorf("the token verification key file %s holds a key that is not an EC key", file)
			}
			keys = append(keys, pub)
			found++
		}
		if found == 0 {
			return nil, fmt.Errorf("the token verification key file %s holds no PEM key", file)
		}
	}
	return keys, nil
}

// Issuer is the "iss" of the tokens v accepts.
func (v *Verifier) Issuer() string { return v.issuer }

// JWKS is the JSON Web Key Set of the keys v holds.
func (v *Verifier) JWKS() []byte { return v.jwks }

// Verify returns the claims of token if the key its "kid" names signed it with
// ES256, its issuer is v's, and now is before its expiry.
func (v *Verifier) Verify(token string, now time.Time) (*api.TokenClaims, error) {
	header, _, ok := strings.Cut(token, ".")
	if !ok {
		return nil, errors.New("the token is not a compact JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return nil, errors.New("the token header is malformed")
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, errors.New("the token header is malformed")
	}
	pub, ok := v.keys[h.Kid]
	if h.Alg != Algorithm || !ok {
		return nil, errors.New("the token is not signed with ES256 by a key of the issuer")
	}
	claims, err := Verify(token, pub)
	if err != nil {
		return nil, err
	}
	if claims.Issuer != v.issuer {
		return nil, errors.New("the token is of another issuer")
	}
	if !now.Before(time.Unix(claims.Expiry, 0)) {
		return nil, errors.New("the token has expired")
	}
	return claims, nil
}

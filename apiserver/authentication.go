package apiserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// tokenFormat is the shape of a bootstrap token: <id>.<secret>.
var tokenFormat = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)

// bootstrapPrefix starts the identity of a bootstrap token holder.
const bootstrapPrefix = "bootstrap:"

type identityKey struct{}

// identity is who made a request: the CN of its client certificate, or
// bootstrap:<id> for a token holder. name is empty where the server
// authenticates nobody, which is plain HTTP.
type identity struct {
	name  string
	token bool
}

func callerOf(r *http.Request) identity {
	id, _ := r.Context().Value(identityKey{}).(identity)
	return id
}

// tokenSet is the bootstrap tokens the server honours.
type tokenSet []string

// identify returns the identity of the holder of the bearer token in header,
// if it is one of the set's tokens.
func (s tokenSet) identify(header string) (identity, bool) {
	presented, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || !tokenFormat.MatchString(presented) {
		return identity{}, false
	}
	found := 0
	var id identity
	for _, t := range s {
		if subtle.ConstantTimeCompare([]byte(t), []byte(presented)) == 1 {
			found = 1
			id = identity{name: bootstrapPrefix + t[:strings.IndexByte(t, '.')], token: true}
		}
	}
	return id, found == 1
}

// bootstrapTokens reads the token file of c. It returns no tokens where c names
// none, and refuses a file given without the TLS files, a file that does not
// read and a line that is not a token.
func (c Config) bootstrapTokens() (tokenSet, error) {
	if err := c.tokenFileNeedsTLS(); err != nil || c.BootstrapTokenFile == "" {
		return nil, err
	}
	data, err := os.ReadFile(c.BootstrapTokenFile)
	if err != nil {
		return nil, fmt.Errorf("read bootstrap token file: %w", err)
	}
	var tokens tokenSet
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !tokenFormat.MatchString(line) {
			return nil, fmt.Errorf("bootstrap token file %s: line %d is not a token of the form <id>.<secret>", c.BootstrapTokenFile, i+1)
		}
		tokens = append(tokens, line)
	}
	return tokens, nil
}

// tokenFileNeedsTLS refuses a token file given without the TLS files, because
// a token is honoured over TLS only.
func (c Config) tokenFileNeedsTLS() error {
	given, err := c.tlsGiven()
	if err != nil {
		return err
	}
	if c.BootstrapTokenFile != "" && !given {
		return errors.New("bootstrap-token-file needs tls-client-ca, tls-cert and tls-key: a token is honoured over TLS only")
	}
	return nil
}

// authenticate identifies the caller of each request before it is routed. Over
// TLS, a request with a verified client certificate is the certificate's CN; one
// without is a bearer-token holder, who may reach only the create and the get
// route of a CertificateSigningRequest, or is refused as Unauthorized. Plain
// HTTP authenticates nobody.
func (h *handler) authenticate(mux *http.ServeMux, tokens tokenSet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id identity
		switch {
		case r.TLS == nil:
		case len(r.TLS.PeerCertificates) > 0:
			id = identity{name: r.TLS.PeerCertificates[0].Subject.CommonName}
		default:
			var ok bool
			if id, ok = tokens.identify(r.Header.Get("Authorization")); !ok {
				h.fail(w, r, api.NewAPIError(api.ReasonUnauthorized, "a client certificate or a valid bearer token is required"))
				return
			}
			if _, pattern := mux.Handler(r); pattern != createCSRRoute && pattern != getCSRRoute {
				h.fail(w, r, api.NewAPIError(api.ReasonForbidden, "a bearer token may only create a certificatesigningrequest and read one it created"))
				return
			}
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

package apiserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/apiserver/internal/token"
	"github.com/garamsh/gaon/lib/api"
)

// tokenFormat is the shape of a bootstrap token: <id>.<secret>.
var tokenFormat = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)

// bootstrapPrefix starts the identity of a bootstrap token holder.
const bootstrapPrefix = "bootstrap:"

type identityKey struct{}

// identity is who made a request: the CN of its client certificate and the
// Organization values of its subject as groups, or bootstrap:<id> for a token
// holder. name is empty where the server authenticates nobody, which is plain
// HTTP.
type identity struct {
	name   string
	groups []string
	token  bool
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

// accountTokens authenticates the bearer tokens of the server's own issuer.
type accountTokens struct {
	verifier *token.Verifier
	// audiences are the audiences the server accepts; a token holds one.
	audiences []string
	// microvm reads the MicroVM a token is bound to.
	microvm func(ctx context.Context, name string) (*api.MicroVM, error)
	now     func() time.Time
}

// identify returns the identity of the holder of the service account token in
// header: the token's subject, where the token verifies, has not expired and
// holds an accepted audience and is bound to a MicroVM that still exists with the uid it names. A token that
// does not hold is not an error; a failure to read the MicroVM is. A nil
// accountTokens authenticates nobody.
func (a *accountTokens) identify(ctx context.Context, header string) (identity, bool, error) {
	presented, ok := strings.CutPrefix(header, "Bearer ")
	if a == nil || !ok {
		return identity{}, false, nil
	}
	claims, err := a.verifier.Verify(presented, a.now())
	if err != nil {
		return identity{}, false, nil
	}
	if !slices.ContainsFunc(claims.Audience, func(aud string) bool { return slices.Contains(a.audiences, aud) }) {
		return identity{}, false, nil
	}
	m, err := a.microvm(ctx, claims.Extension.MicroVM.Name)
	if errors.Is(err, api.ErrNotFound) {
		return identity{}, false, nil
	}
	if err != nil {
		return identity{}, false, err
	}
	if m.Metadata.UID != claims.Extension.MicroVM.UID {
		return identity{}, false, nil
	}
	return identity{name: claims.Subject}, true, nil
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
// without is a holder of a service account token, who is its subject, or of a
// bootstrap token, who may reach only the create and the get route of a
// CertificateSigningRequest, or is refused as Unauthorized. Plain HTTP
// authenticates nobody. The discovery document and the key set are served to
// every caller, with no identity.
func (h *handler) authenticate(mux *http.ServeMux, tokens tokenSet, accounts *accountTokens) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == discoveryRoute || pattern == jwksRoute {
			mux.ServeHTTP(w, r)
			return
		}
		var id identity
		switch {
		case r.TLS == nil:
		case len(r.TLS.PeerCertificates) > 0:
			subject := r.TLS.PeerCertificates[0].Subject
			id = identity{name: subject.CommonName, groups: subject.Organization}
		default:
			header := r.Header.Get("Authorization")
			var ok bool
			if id, ok = tokens.identify(header); ok {
				if _, pattern := mux.Handler(r); pattern != createCSRRoute && pattern != getCSRRoute {
					h.fail(w, r, api.NewAPIError(api.ReasonForbidden, "a bearer token may only create a certificatesigningrequest and read one it created"))
					return
				}
				break
			}
			var err error
			if id, ok, err = accounts.identify(r.Context(), header); err != nil {
				h.fail(w, r, err)
				return
			}
			if !ok {
				h.fail(w, r, api.NewAPIError(api.ReasonUnauthorized, "a client certificate or a valid bearer token is required"))
				return
			}
		}
		ctx := context.WithValue(r.Context(), identityKey{}, id)
		ctx = resourceapi.WithUser(ctx, api.UserInfo{Username: id.name, Groups: id.groups})
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

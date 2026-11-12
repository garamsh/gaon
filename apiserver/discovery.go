package apiserver

import (
	"encoding/json"
	"net/http"

	"github.com/garamsh/gaon/apiserver/internal/token"
)

// The paths of the issuer's OpenID Connect discovery document and of the key
// set it names, served at the server's root to every caller.
const (
	discoveryPath = "/.well-known/openid-configuration"
	jwksPath      = "/openid/v1/jwks"

	discoveryRoute = "GET " + discoveryPath
	jwksRoute      = "GET " + jwksPath
)

// serveDiscovery registers the discovery document and the key set of v on mux.
func serveDiscovery(mux *http.ServeMux, v *token.Verifier) {
	doc, err := json.Marshal(map[string]any{
		"issuer":                                v.Issuer(),
		"jwks_uri":                              v.Issuer() + jwksPath,
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{token.Algorithm},
	})
	if err != nil {
		panic(err)
	}
	mux.HandleFunc(discoveryRoute, serveJSON(doc))
	mux.HandleFunc(jwksRoute, serveJSON(v.JWKS()))
}

func serveJSON(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

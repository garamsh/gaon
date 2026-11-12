package fakegaon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// SetServiceAccount stores the ServiceAccount name, which a token request
// names.
func (s *Server) SetServiceAccount(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[name] = true
}

// FailTokens makes every token request answer with an internal error, as
// when the API server cannot sign.
func (s *Server) FailTokens(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures["tokens"] = on
}

// TokenRequests returns the specs of the token requests received, in order,
// the refused ones included.
func (s *Server) TokenRequests() []api.TokenRequestSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.TokenRequestSpec(nil), s.requests...)
}

// IssuedTokens returns the tokens issued, in order. Each is "token-<n>".
func (s *Server) IssuedTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.issued...)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req api.TokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "decode: %v", err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req.Spec)
	if s.failures["tokens"] {
		writeError(w, api.NewAPIError(api.ReasonInternalError, "cannot sign"))
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "%v", err))
		return
	}
	if !s.accounts[r.PathValue("name")] {
		notFound(w, r.PathValue("name"))
		return
	}
	m, ok := s.microvms[req.Spec.BoundMicroVM.Name]
	if !ok || m.Metadata.UID != req.Spec.BoundMicroVM.UID {
		writeError(w, api.NewAPIError(api.ReasonConflict, "the MicroVM bound is not the one named"))
		return
	}
	token := fmt.Sprintf("token-%d", len(s.issued)+1)
	s.issued = append(s.issued, token)
	expires := time.Now().Add(req.Spec.Lifetime())
	req.Status = api.TokenRequestStatus{Token: token, ExpirationTimestamp: &expires}
	writeJSON(w, http.StatusOK, req)
}

package fakegaon

import (
	"net/http"

	"github.com/garamsh/gaon/lib/api"
)

// SetSecret stores sec under a new version, as a user does.
func (s *Server) SetSecret(sec api.Secret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecret}
	s.rev++
	sec.Metadata.ResourceVersion = version(s.rev)
	s.secrets[sec.Metadata.Name] = sec
}

// SetConfigMap stores c under a new version, as a user does.
func (s *Server) SetConfigMap(c api.ConfigMap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindConfigMap}
	s.rev++
	c.Metadata.ResourceVersion = version(s.rev)
	s.configs[c.Metadata.Name] = c
}

// Reads returns the requests for one Secret or one ConfigMap, as "GET path",
// in order. The fake serves no list or watch of either kind, as the Node
// authorizer allows a Node none.
func (s *Server) Reads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...)
}

func (s *Server) getSecret(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, "GET "+r.URL.Path)
	sec, ok := s.secrets[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, sec)
}

func (s *Server) getConfigMap(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, "GET "+r.URL.Path)
	c, ok := s.configs[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, c)
}

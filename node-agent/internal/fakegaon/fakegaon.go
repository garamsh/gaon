// Package fakegaon is an in-memory HTTP server for the endpoints of the
// resource API the node agent uses, for its tests.
package fakegaon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

type logged struct {
	rev   int64
	event api.WatchEvent
}

// Server is the fake. Its methods are safe for concurrent use.
type Server struct {
	// URL is the root a client is given.
	URL string

	mu        sync.Mutex
	rev       int64
	microvms  map[string]api.MicroVM
	volumes   map[string]api.Volume
	secrets   map[string]api.Secret
	configs   map[string]api.ConfigMap
	policies  map[string]api.NetworkPolicy
	services  map[string]api.Service
	endpoints map[string]api.Endpoints
	reads     []string
	nodes     map[string]api.Node
	leases    map[string]api.Lease
	log       []logged
	changed   chan struct{}
	failures  map[string]bool
	puts      []api.Lease
	node      string
	holdNode  bool
	attempts  int
	refused   []string
	csrs      map[string]api.CertificateSigningRequest
	tls       *tlsState
	accounts  map[string]bool
	requests  []api.TokenRequestSpec
	issued    []string
}

// New starts a fake and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s, mux := newServer()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.URL = ts.URL
	return s
}

func newServer() (*Server, *http.ServeMux) {
	s := &Server{
		microvms:  map[string]api.MicroVM{},
		volumes:   map[string]api.Volume{},
		secrets:   map[string]api.Secret{},
		configs:   map[string]api.ConfigMap{},
		policies:  map[string]api.NetworkPolicy{},
		services:  map[string]api.Service{},
		endpoints: map[string]api.Endpoints{},
		nodes:     map[string]api.Node{},
		leases:    map[string]api.Lease{},
		csrs:      map[string]api.CertificateSigningRequest{},
		changed:   make(chan struct{}),
		failures:  map[string]bool{},
		accounts:  map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /microvms", s.listMicroVMs)
	mux.HandleFunc("GET /microvms/{name}", s.getMicroVM)
	mux.HandleFunc("PUT /microvms/{name}", s.putMicroVM)
	mux.HandleFunc("PUT /microvms/{name}/status", s.putMicroVMStatus)
	mux.HandleFunc("GET /volumes", s.listVolumes)
	mux.HandleFunc("GET /volumes/{name}", s.getVolume)
	mux.HandleFunc("PUT /volumes/{name}", s.putVolume)
	mux.HandleFunc("PUT /volumes/{name}/status", s.putVolumeStatus)
	mux.HandleFunc("GET /secrets/{name}", s.getSecret)
	mux.HandleFunc("GET /configmaps/{name}", s.getConfigMap)
	mux.HandleFunc("GET /networkpolicies", s.listNetworkPolicies)
	mux.HandleFunc("GET /services", s.listServices)
	mux.HandleFunc("GET /endpoints", s.listEndpoints)
	mux.HandleFunc("POST /serviceaccounts/{name}/token", s.createToken)
	mux.HandleFunc("GET /nodes/{name}", s.getNode)
	mux.HandleFunc("PUT /nodes/{name}", s.putNode)
	mux.HandleFunc("PUT /nodes/{name}/status", s.putNodeStatus)
	mux.HandleFunc("GET /leases/{name}", s.getLease)
	mux.HandleFunc("PUT /leases/{name}", s.putLease)
	mux.HandleFunc("PUT /certificatesigningrequests/{name}", s.putCSR)
	mux.HandleFunc("GET /certificatesigningrequests/{name}", s.getCSR)
	return s, mux
}

// FailLeaseWrites makes every Lease write fail with a server error while on
// is true.
func (s *Server) FailLeaseWrites(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures["leases"] = on
}

// RequireNode makes the server refuse, as the API server's RBAC does, a
// MicroVM list or watch and a Lease write while the Node name does not exist,
// and records each refusal.
func (s *Server) RequireNode(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.node = name
}

// HoldNodeCreation makes every attempt to create a Node fail with a server
// error while on is true.
func (s *Server) HoldNodeCreation(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holdNode = on
}

// NodeCreations returns how many times a Node has been asked for, by a PUT
// without a version, whether or not it was created.
func (s *Server) NodeCreations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// Refused returns the requests refused for the absence of the Node named by
// RequireNode, as "METHOD path", in order.
func (s *Server) Refused() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refused...)
}

// SetNode stores n, as a user or an earlier agent does.
func (s *Server) SetNode(n api.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode}
	s.rev++
	n.Metadata.ResourceVersion = version(s.rev)
	s.nodes[n.Metadata.Name] = n
}

// refuseWithoutNode answers a request that needs the Node named by
// RequireNode with the refusal its absence gives, and reports whether it did.
// The caller holds s.mu.
func (s *Server) refuseWithoutNode(w http.ResponseWriter, r *http.Request, e *api.APIError) bool {
	if _, ok := s.nodes[s.node]; s.node == "" || ok {
		return false
	}
	s.refused = append(s.refused, r.Method+" "+r.URL.Path)
	writeError(w, e)
	return true
}

// MicroVM returns the stored MicroVM name.
func (s *Server) MicroVM(name string) api.MicroVM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.microvms[name]
}

// Node returns the stored Node name, and whether it exists.
func (s *Server) Node(name string) (api.Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[name]
	return n, ok
}

// Lease returns the stored Lease name, and whether it exists.
func (s *Server) Lease(name string) (api.Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[name]
	return l, ok
}

// LeaseWrites returns the Leases written, in order.
func (s *Server) LeaseWrites() []api.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.Lease(nil), s.puts...)
}

// SetNodeAdmitted sets spec.admitted of the stored Node, as a user does.
func (s *Server) SetNodeAdmitted(name string, admitted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.nodes[name]
	n.Spec.Admitted = admitted
	s.rev++
	n.Metadata.ResourceVersion = version(s.rev)
	s.nodes[name] = n
}

// SetMicroVM stores m, as a user or the Scheduler does, and emits Added for
// a MicroVM that is new and Modified for one that is not.
func (s *Server) SetMicroVM(m api.MicroVM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM}
	t := api.EventAdded
	if _, ok := s.microvms[m.Metadata.Name]; ok {
		t = api.EventModified
	}
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.microvms[m.Metadata.Name] = m
	s.record(t, &m)
}

// BumpVersion changes the stored version of the MicroVM name without emitting
// an event, as a write does that a watcher has not yet been told of.
func (s *Server) BumpVersion(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.microvms[name]
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.microvms[name] = m
}

// MarkDeleting sets deletionTimestamp on the MicroVM name and emits Modified,
// as a delete does while a finalizer is held.
func (s *Server) MarkDeleting(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.microvms[name]
	now := time.Now().UTC()
	m.Metadata.DeletionTimestamp = &now
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.microvms[name] = m
	s.record(api.EventModified, &m)
}

// DeleteMicroVM removes the MicroVM name and emits Deleted.
func (s *Server) DeleteMicroVM(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.microvms[name]
	delete(s.microvms, name)
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.record(api.EventDeleted, &m)
}

// Inject emits an event for m without storing it, as a delayed or replayed
// event reaches a watcher.
func (s *Server) Inject(t api.EventType, m api.MicroVM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM}
	s.rev++
	s.record(t, &m)
}

func (s *Server) record(t api.EventType, m *api.MicroVM) {
	ev, _ := api.NewEvent(t, m)
	s.log = append(s.log, logged{rev: s.rev, event: ev})
	close(s.changed)
	s.changed = make(chan struct{})
}

func version(rev int64) api.ResourceVersion { return api.ResourceVersion(strconv.FormatInt(rev, 10)) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *api.APIError) { writeJSON(w, e.Code, e) }

func notFound(w http.ResponseWriter, name string) {
	writeError(w, api.NewAPIError(api.ReasonNotFound, "%q not found", name))
}

// checkVersion answers the compare-and-swap of a write: an empty version
// creates and conflicts with an existing object, any other must match.
func checkVersion(found bool, stored, got api.ResourceVersion, name string) *api.APIError {
	switch {
	case got == "" && found:
		return api.NewAPIError(api.ReasonAlreadyExists, "%q exists", name)
	case got != "" && !found:
		return api.NewAPIError(api.ReasonNotFound, "%q not found", name)
	case got != "" && got != stored:
		return api.NewAPIError(api.ReasonConflict, "%q is at version %s, not %s", name, stored, got)
	}
	return nil
}

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

	mu       sync.Mutex
	rev      int64
	machines map[string]api.Machine
	nodes    map[string]api.Node
	leases   map[string]api.Lease
	log      []logged
	changed  chan struct{}
	failures map[string]bool
	puts     []api.Lease
}

// New starts a fake and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		machines: map[string]api.Machine{},
		nodes:    map[string]api.Node{},
		leases:   map[string]api.Lease{},
		changed:  make(chan struct{}),
		failures: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /machines", s.listMachines)
	mux.HandleFunc("PUT /machines/{name}/status", s.putMachineStatus)
	mux.HandleFunc("GET /nodes/{name}", s.getNode)
	mux.HandleFunc("PUT /nodes/{name}", s.putNode)
	mux.HandleFunc("PUT /nodes/{name}/status", s.putNodeStatus)
	mux.HandleFunc("GET /leases/{name}", s.getLease)
	mux.HandleFunc("PUT /leases/{name}", s.putLease)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.URL = ts.URL
	return s
}

// FailLeaseWrites makes every Lease write fail with a server error while on
// is true.
func (s *Server) FailLeaseWrites(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures["leases"] = on
}

// Machine returns the stored Machine name.
func (s *Server) Machine(name string) api.Machine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.machines[name]
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

// SetMachine stores m, as a user or the Scheduler does, and emits Added for
// a Machine that is new and Modified for one that is not.
func (s *Server) SetMachine(m api.Machine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine}
	t := api.EventAdded
	if _, ok := s.machines[m.Metadata.Name]; ok {
		t = api.EventModified
	}
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.machines[m.Metadata.Name] = m
	s.record(t, &m)
}

// DeleteMachine removes the Machine name and emits Deleted.
func (s *Server) DeleteMachine(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machines[name]
	delete(s.machines, name)
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.record(api.EventDeleted, &m)
}

// Inject emits an event for m without storing it, as a delayed or replayed
// event reaches a watcher.
func (s *Server) Inject(t api.EventType, m api.Machine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine}
	s.rev++
	s.record(t, &m)
}

func (s *Server) record(t api.EventType, m *api.Machine) {
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

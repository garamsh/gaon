package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// memoryServer is an in-memory resource API server for the client tests: the
// paths of resource-api.md §Paths over a map, one revision counter for the
// whole store, and a watch log that compact trims.
type memoryServer struct {
	mu        sync.Mutex
	rev       int64
	objects   map[string][]byte
	log       []loggedEvent
	compacted int64
	changed   chan struct{}
}

type loggedEvent struct {
	rev   int64
	event api.WatchEvent
}

func newMemoryServer(t *testing.T) (*memoryServer, *api.Client) {
	t.Helper()
	s := &memoryServer{objects: map[string][]byte{}, changed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /machines/{name}", s.putMachine)
	mux.HandleFunc("POST /machines/{name}/binding", s.bindMachine)
	mux.HandleFunc("PUT /machines/{name}/status", s.putMachineStatus)
	mux.HandleFunc("DELETE /machines/{name}", s.deleteMachine)
	mux.HandleFunc("GET /machines/{name}", s.getMachine)
	mux.HandleFunc("GET /machines", s.listOrWatchMachines)
	mux.HandleFunc("PUT /volumes/{name}/status", s.putVolumeStatus)
	mux.HandleFunc("PUT /leases/{name}", s.putLease)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, api.NewClient(ts.URL, ts.Client())
}

// compact drops the watch log, so a watch from an earlier version expires.
func (s *memoryServer) compact() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compacted = s.rev
	s.log = nil
}

// seedVolume stores v, which has no create path in the contract.
func (s *memoryServer) seedVolume(v *api.Volume) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects["volumes/"+v.Metadata.Name] = mustJSON(v)
}

func (s *memoryServer) putMachine(w http.ResponseWriter, r *http.Request) {
	var m api.Machine
	if !decodeBody(w, r, &m) || !s.nameMatches(w, r, m.Metadata.Name) {
		return
	}
	if err := m.Validate(); err != nil {
		writeError(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, found := s.loadMachine(m.Metadata.Name)
	if err := s.checkVersion(found, old.Metadata.ResourceVersion, m.Metadata.ResourceVersion, m.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if found {
		if err := m.ValidateUpdate(&old); err != nil {
			writeError(w, err)
			return
		}
		m.Status = old.Status
		m.Spec.NodeName, m.Spec.BindingEpoch = old.Spec.NodeName, old.Spec.BindingEpoch
		s.storeMachine(w, &m, api.EventModified)
		return
	}
	m.Status = api.MachineStatus{Phase: api.MachinePending}
	s.storeMachine(w, &m, api.EventAdded)
}

func (s *memoryServer) bindMachine(w http.ResponseWriter, r *http.Request) {
	var b api.Binding
	if !decodeBody(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMachine(r.PathValue("name"))
	if err := s.requireVersion(found, m.Metadata.ResourceVersion, b.ResourceVersion, r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	if err := m.Bind(b); err != nil {
		writeError(w, err)
		return
	}
	s.storeMachine(w, &m, api.EventModified)
}

func (s *memoryServer) putMachineStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Machine
	if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMachine(next.Metadata.Name)
	if err := s.requireVersion(found, m.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatusUpdate(&m); err != nil {
		writeError(w, err)
		return
	}
	m.Status = next.Status
	s.storeMachine(w, &m, api.EventModified)
}

func (s *memoryServer) deleteMachine(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMachine(name)
	if err := s.requireVersion(found, m.Metadata.ResourceVersion, api.ResourceVersion(r.URL.Query().Get("resourceVersion")), name); err != nil {
		writeError(w, err)
		return
	}
	now := time.Now().UTC()
	m.Metadata.DeletionTimestamp = &now
	if len(m.Metadata.Finalizers) > 0 {
		s.storeMachine(w, &m, api.EventModified)
		return
	}
	delete(s.objects, "machines/"+name)
	s.rev++
	m.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.record(&m, api.EventDeleted)
	writeJSON(w, http.StatusOK, &m)
}

func (s *memoryServer) getMachine(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMachine(r.PathValue("name"))
	if !found {
		writeError(w, api.NewAPIError(api.ReasonNotFound, "machine %q not found", r.PathValue("name")))
		return
	}
	writeJSON(w, http.StatusOK, &m)
}

func (s *memoryServer) listOrWatchMachines(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		s.watchMachines(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := api.MachineList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachineList},
		Metadata: api.ListMeta{ResourceVersion: api.ResourceVersion(strconv.FormatInt(s.rev, 10))},
		Items:    []api.Machine{},
	}
	for key := range s.objects {
		if name, ok := strings.CutPrefix(key, "machines/"); ok {
			m, _ := s.loadMachine(name)
			list.Items = append(list.Items, m)
		}
	}
	writeJSON(w, http.StatusOK, &list)
}

func (s *memoryServer) watchMachines(w http.ResponseWriter, r *http.Request) {
	from, err := strconv.ParseInt(r.URL.Query().Get("resourceVersion"), 10, 64)
	if err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "resourceVersion is not a version"))
		return
	}
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	enc := json.NewEncoder(w)
	for {
		s.mu.Lock()
		if compacted := s.compacted; from < compacted {
			s.mu.Unlock()
			ev, _ := api.NewErrorEvent(api.NewAPIError(api.ReasonExpired, "resourceVersion %d is older than %d", from, compacted))
			_ = enc.Encode(ev)
			flusher.Flush()
			return
		}
		var pending []loggedEvent
		for _, l := range s.log {
			if l.rev > from {
				pending = append(pending, l)
			}
		}
		changed := s.changed
		s.mu.Unlock()
		for _, l := range pending {
			if err := enc.Encode(l.event); err != nil {
				return
			}
			from = l.rev
		}
		flusher.Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

func (s *memoryServer) putVolumeStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Volume
	if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "volumes/" + next.Metadata.Name
	raw, found := s.objects[key]
	var v api.Volume
	if found {
		_ = json.Unmarshal(raw, &v)
	}
	if err := s.requireVersion(found, v.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatus(); err != nil {
		writeError(w, err)
		return
	}
	v.Status = next.Status
	s.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects[key] = mustJSON(&v)
	writeJSON(w, http.StatusOK, &v)
}

func (s *memoryServer) putLease(w http.ResponseWriter, r *http.Request) {
	var l api.Lease
	if !decodeBody(w, r, &l) || !s.nameMatches(w, r, l.Metadata.Name) {
		return
	}
	if err := l.Validate(); err != nil {
		writeError(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "leases/" + l.Metadata.Name
	var old api.Lease
	raw, found := s.objects[key]
	if found {
		_ = json.Unmarshal(raw, &old)
	}
	if err := s.checkVersion(found, old.Metadata.ResourceVersion, l.Metadata.ResourceVersion, l.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	s.rev++
	l.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects[key] = mustJSON(&l)
	writeJSON(w, http.StatusOK, &l)
}

func (s *memoryServer) loadMachine(name string) (api.Machine, bool) {
	var m api.Machine
	raw, found := s.objects["machines/"+name]
	if found {
		_ = json.Unmarshal(raw, &m)
	}
	return m, found
}

// storeMachine writes m at a new revision, logs the event and answers with m.
func (s *memoryServer) storeMachine(w http.ResponseWriter, m *api.Machine, t api.EventType) {
	s.rev++
	m.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects["machines/"+m.Metadata.Name] = mustJSON(m)
	s.record(m, t)
	writeJSON(w, http.StatusOK, m)
}

func (s *memoryServer) record(m *api.Machine, t api.EventType) {
	ev, err := api.NewMachineEvent(t, m)
	if err != nil {
		panic(err)
	}
	s.log = append(s.log, loggedEvent{rev: s.rev, event: ev})
	close(s.changed)
	s.changed = make(chan struct{})
}

// checkVersion is the write rule of PUT: an empty version creates, and
// refuses an object that exists; a version updates, and must be current.
func (s *memoryServer) checkVersion(found bool, current, given api.ResourceVersion, name string) error {
	if given == "" {
		if found {
			return api.NewAPIError(api.ReasonAlreadyExists, "%q already exists", name)
		}
		return nil
	}
	return s.requireVersion(found, current, given, name)
}

// requireVersion is the write rule of every other write: the object exists
// at the given version.
func (s *memoryServer) requireVersion(found bool, current, given api.ResourceVersion, name string) error {
	if !found {
		return api.NewAPIError(api.ReasonNotFound, "%q not found", name)
	}
	if current != given {
		return api.NewAPIError(api.ReasonConflict, "%q is at version %s, not %s", name, current, given)
	}
	return nil
}

func (s *memoryServer) nameMatches(w http.ResponseWriter, r *http.Request, name string) bool {
	if name != r.PathValue("name") {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "metadata.name %q differs from the path", name))
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "decode body: %v", err))
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	apiErr, ok := err.(*api.APIError)
	if !ok {
		apiErr = &api.APIError{Code: http.StatusInternalServerError, Reason: "Internal", Message: err.Error()}
	}
	writeJSON(w, apiErr.Code, apiErr)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode %T: %v", v, err))
	}
	return b
}

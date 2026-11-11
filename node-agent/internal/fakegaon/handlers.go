package fakegaon

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
)

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "decode body: %v", err))
		return false
	}
	return true
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		s.watch(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := api.MachineList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachineList},
		Metadata: api.ListMeta{ResourceVersion: version(s.rev)},
		Items:    []api.Machine{},
	}
	for _, m := range s.machines {
		list.Items = append(list.Items, m)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getMachine(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	from, err := strconv.ParseInt(r.URL.Query().Get("resourceVersion"), 10, 64)
	if err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "resourceVersion is not a version"))
		return
	}
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	enc := json.NewEncoder(w)
	flusher.Flush()
	for {
		s.mu.Lock()
		var pending []logged
		for _, l := range s.log {
			if l.rev > from {
				pending = append(pending, l)
			}
		}
		changed := s.changed
		s.mu.Unlock()
		for _, l := range pending {
			from = l.rev
			if enc.Encode(l.event) != nil {
				return
			}
		}
		flusher.Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

// putMachine changes the spec and the finalizers of a Machine, checking the
// version and, as the API server does, refusing a finalizer added to a
// Machine being deleted and removing a Machine whose last finalizer goes.
func (s *Server) putMachine(w http.ResponseWriter, r *http.Request) {
	var next api.Machine
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.machines[next.Metadata.Name]
	if !found {
		notFound(w, next.Metadata.Name)
		return
	}
	if err := checkVersion(found, m.Metadata.ResourceVersion, next.Metadata.ResourceVersion, m.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	deleting := m.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(next.Metadata.Finalizers, func(f string) bool { return !slices.Contains(m.Metadata.Finalizers, f) }) {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to a machine being deleted"))
		return
	}
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	if deleting && len(next.Metadata.Finalizers) == 0 {
		m.Metadata.Finalizers = nil
		delete(s.machines, m.Metadata.Name)
		s.record(api.EventDeleted, &m)
		writeJSON(w, http.StatusOK, m)
		return
	}
	m.Spec, m.Metadata.Finalizers = next.Spec, next.Metadata.Finalizers
	s.machines[m.Metadata.Name] = m
	s.record(api.EventModified, &m)
	writeJSON(w, http.StatusOK, m)
}

// putMachineStatus replaces the status of a Machine, checking the version
// and, as the API server does, the epoch.
func (s *Server) putMachineStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Machine
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.machines[next.Metadata.Name]
	if !found {
		notFound(w, next.Metadata.Name)
		return
	}
	if err := checkVersion(found, m.Metadata.ResourceVersion, next.Metadata.ResourceVersion, m.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatusUpdate(&m); err != nil {
		writeError(w, err.(*api.APIError))
		return
	}
	m.Status = next.Status
	s.rev++
	m.Metadata.ResourceVersion = version(s.rev)
	s.machines[m.Metadata.Name] = m
	s.record(api.EventModified, &m)
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// putNode creates a Node, with status Discovered as the API server sets it,
// or changes the spec of one.
func (s *Server) putNode(w http.ResponseWriter, r *http.Request) {
	var next api.Node
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, found := s.nodes[next.Metadata.Name]
	if err := checkVersion(found, n.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if !found {
		n.Status.State = api.NodeStateDiscovered
	}
	n.TypeMeta, n.Metadata, n.Spec = next.TypeMeta, next.Metadata, next.Spec
	s.rev++
	n.Metadata.ResourceVersion = version(s.rev)
	s.nodes[n.Metadata.Name] = n
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) putNodeStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Node
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, found := s.nodes[next.Metadata.Name]
	if !found {
		notFound(w, next.Metadata.Name)
		return
	}
	if err := checkVersion(found, n.Metadata.ResourceVersion, next.Metadata.ResourceVersion, n.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatus(); err != nil {
		writeError(w, err.(*api.APIError))
		return
	}
	n.Status = next.Status
	s.rev++
	n.Metadata.ResourceVersion = version(s.rev)
	s.nodes[n.Metadata.Name] = n
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) getLease(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) putLease(w http.ResponseWriter, r *http.Request) {
	var next api.Lease
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures["leases"] {
		writeError(w, &api.APIError{Code: http.StatusServiceUnavailable, Reason: "Unavailable", Message: "store is down"})
		return
	}
	l, found := s.leases[next.Metadata.Name]
	if err := checkVersion(found, l.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	s.rev++
	next.Metadata.ResourceVersion = version(s.rev)
	s.leases[next.Metadata.Name] = next
	s.puts = append(s.puts, next)
	writeJSON(w, http.StatusOK, next)
}

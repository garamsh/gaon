package fakegaon

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// SetVolume stores v under a new version, as a user or a binding does.
func (s *Server) SetVolume(v api.Volume) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume}
	s.rev++
	v.Metadata.ResourceVersion = version(s.rev)
	s.volumes[v.Metadata.Name] = v
}

// Volume returns the stored Volume name, and whether it exists.
func (s *Server) Volume(name string) (api.Volume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[name]
	return v, ok
}

// MarkVolumeDeleting sets deletionTimestamp on the Volume name, as a delete
// does while a finalizer is held.
func (s *Server) MarkVolumeDeleting(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.volumes[name]
	now := time.Now().UTC()
	v.Metadata.DeletionTimestamp = &now
	s.rev++
	v.Metadata.ResourceVersion = version(s.rev)
	s.volumes[name] = v
}

// listVolumes answers the Volumes ordered by name, so a test can tell what
// one pass of a loop has seen of them.
func (s *Server) listVolumes(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := api.VolumeList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolumeList},
		Metadata: api.ListMeta{ResourceVersion: version(s.rev)},
		Items:    []api.Volume{},
	}
	for _, v := range s.volumes {
		list.Items = append(list.Items, v)
	}
	slices.SortFunc(list.Items, func(a, b api.Volume) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getVolume(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putVolume changes the finalizers of a Volume, checking the version and, as
// the API server does, refusing a finalizer added to a Volume being deleted
// and removing a Volume whose last finalizer goes. It changes nothing else.
func (s *Server) putVolume(w http.ResponseWriter, r *http.Request) {
	var next api.Volume
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, found := s.volumes[next.Metadata.Name]
	if !found {
		notFound(w, next.Metadata.Name)
		return
	}
	if err := checkVersion(found, v.Metadata.ResourceVersion, next.Metadata.ResourceVersion, v.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateUpdate(&v); err != nil {
		writeError(w, err.(*api.APIError))
		return
	}
	deleting := v.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(next.Metadata.Finalizers, func(f string) bool { return !slices.Contains(v.Metadata.Finalizers, f) }) {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to a volume being deleted"))
		return
	}
	s.rev++
	v.Metadata.ResourceVersion = version(s.rev)
	if deleting && len(next.Metadata.Finalizers) == 0 {
		v.Metadata.Finalizers = nil
		delete(s.volumes, v.Metadata.Name)
		writeJSON(w, http.StatusOK, v)
		return
	}
	v.Metadata.Finalizers = next.Metadata.Finalizers
	s.volumes[v.Metadata.Name] = v
	writeJSON(w, http.StatusOK, v)
}

// putVolumeStatus replaces the status of a Volume, checking the version and,
// as the API server does, the phase and a new attachment: it must name the
// current epoch of an existing Machine and pass the Volume's attach rule.
func (s *Server) putVolumeStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Volume
	if !decode(w, r, &next) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, found := s.volumes[next.Metadata.Name]
	if !found {
		notFound(w, next.Metadata.Name)
		return
	}
	if err := checkVersion(found, v.Metadata.ResourceVersion, next.Metadata.ResourceVersion, v.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatus(); err != nil {
		writeError(w, err.(*api.APIError))
		return
	}
	if a := next.Status.Attachment; a != nil && (v.Status.Attachment == nil || *a != *v.Status.Attachment) {
		m, ok := s.machines[a.MachineName]
		if !ok || m.Spec.BindingEpoch != a.BindingEpoch {
			writeError(w, api.NewAPIError(api.ReasonInvalid, "attachment names machine %q at epoch %d, which is not a Machine's current binding", a.MachineName, a.BindingEpoch))
			return
		}
		probe := v
		if err := probe.Attach(a.MachineName, a.BindingEpoch); err != nil {
			writeError(w, err.(*api.APIError))
			return
		}
	}
	v.Status = next.Status
	s.rev++
	v.Metadata.ResourceVersion = version(s.rev)
	s.volumes[v.Metadata.Name] = v
	writeJSON(w, http.StatusOK, v)
}

package job_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

func (f *fakeAPI) statefulSetsClient() *api.CustomResourceClient {
	c, err := api.NewCustomResourceClient(f.server.URL, nil, job.StatefulSetType)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// addStatefulSet stores a StatefulSet of generation 1 with the spec JSON given.
func (f *fakeAPI) addStatefulSet(name string, uid api.UID, spec string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statefulsets[name] = &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.KindStatefulSet},
		Metadata: api.ObjectMeta{Name: name, UID: uid, Generation: 1, ResourceVersion: f.rv()},
		Spec:     json.RawMessage(spec),
	}
}

// setStatefulSetSpec changes a stored StatefulSet's spec, as a user's update does.
func (f *fakeAPI) setStatefulSetSpec(name, spec string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.statefulsets[name]
	s.Spec = json.RawMessage(spec)
	s.Metadata.Generation++
	s.Metadata.ResourceVersion = f.rv()
}

// statefulSetStatus returns the status the service wrote to the StatefulSet.
func (f *fakeAPI) statefulSetStatus(name string) job.StatefulSetStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s job.StatefulSetStatus
	if raw := f.statefulsets[name].Status; len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			f.t.Fatal(err)
		}
	}
	return s
}

// volume returns a copy of the stored Volume, or nil.
func (f *fakeAPI) volume(name string) *api.Volume {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.volumes[name]
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func (f *fakeAPI) volumeNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for n := range f.volumes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (f *fakeAPI) volumeWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumePuts
}

// makeReady marks the named MicroVM Ready, as its node agent does.
func (f *fakeAPI) makeReady(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range names {
		m := f.microvms[n]
		markReady(m)
		m.Metadata.ResourceVersion = f.rv()
	}
}

// markDeleting sets the deletionTimestamp of a stored MicroVM, which stays
// stored until removeMicroVM: a node agent that has not yet released it.
func (f *fakeAPI) markDeleting(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	m := f.microvms[name]
	m.Metadata.DeletionTimestamp = &now
	m.Metadata.ResourceVersion = f.rv()
}

// addSetReplica stores a MicroVM owned by the StatefulSet, Ready or not.
func (f *fakeAPI) addSetReplica(name, set string, uid api.UID, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{
			Name: name, ResourceVersion: f.rv(),
			OwnerReferences: []api.OwnerReference{{APIVersion: job.APIVersion, Kind: job.KindStatefulSet, Name: set, UID: uid, Controller: true}},
		},
	}
	if ready {
		markReady(m)
	}
	f.microvms[name] = m
}

func (f *fakeAPI) listStatefulSets(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	for _, o := range f.statefulsets {
		list.Items = append(list.Items, *o)
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) putStatefulSetStatus(w http.ResponseWriter, r *http.Request) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.statefulsets[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "statefulset"))
		return
	}
	if o.Metadata.ResourceVersion != in.Metadata.ResourceVersion {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "statefulset changed"))
		return
	}
	o.Status = in.Status
	o.Metadata.ResourceVersion = f.rv()
	writeJSON(w, http.StatusOK, o)
}

func (f *fakeAPI) listVolumes(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.VolumeList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.Volume{}}
	for _, v := range f.volumes {
		list.Items = append(list.Items, *v)
	}
	writeJSON(w, http.StatusOK, list)
}

// putVolume creates a Volume and answers 409 for one that exists, as the
// server does for a create.
func (f *fakeAPI) putVolume(w http.ResponseWriter, r *http.Request) {
	var v api.Volume
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumePuts++
	if f.volumes[v.Metadata.Name] != nil {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonAlreadyExists, "volume"))
		return
	}
	v.Metadata.ResourceVersion = f.rv()
	f.volumes[v.Metadata.Name] = &v
	writeJSON(w, http.StatusCreated, &v)
}

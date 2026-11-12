package job_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

// fakeAPI serves what the Job service uses: the definitions, the kinds, the
// MicroVMs and the Volumes. A write is stored as sent, as a server stores an
// object with no admission, and a PUT of a MicroVM that exists answers 409.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu               sync.Mutex
	definitions      map[string]*api.CustomResourceDefinition
	jobs             map[string]*api.CustomObject
	cronjobs         map[string]*api.CustomObject
	deployments      map[string]*api.CustomObject
	statefulsets     map[string]*api.CustomObject
	volumes          map[string]*api.Volume
	volumePuts       int
	microvms         map[string]*api.MicroVM
	definitionWrites int
	statusWrites     int
	nextRV           int
	invalidMicroVM   bool
	// bounds, when a test sets it, is checked after every write to a MicroVM.
	bounds *rolloutBounds
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t: t, definitions: map[string]*api.CustomResourceDefinition{}, jobs: map[string]*api.CustomObject{},
		cronjobs: map[string]*api.CustomObject{}, deployments: map[string]*api.CustomObject{},
		statefulsets: map[string]*api.CustomObject{}, volumes: map[string]*api.Volume{}, microvms: map[string]*api.MicroVM{}, nextRV: 100,
	}
	mux := http.NewServeMux()
	ext := "/apis/" + api.APIExtensionsGroupVersion + "/customresourcedefinitions/{name}"
	mux.HandleFunc("GET "+ext, f.getDefinition)
	mux.HandleFunc("PUT "+ext, f.putDefinition)
	jobs := "/apis/" + job.APIVersion + "/jobs"
	mux.HandleFunc("GET "+jobs, f.listJobs)
	mux.HandleFunc("PUT "+jobs+"/{name}/status", f.putJobStatus)
	mux.HandleFunc("PUT "+jobs+"/{name}", f.putJob)
	mux.HandleFunc("DELETE "+jobs+"/{name}", f.deleteJob)
	cronjobs := "/apis/" + job.APIVersion + "/cronjobs"
	mux.HandleFunc("GET "+cronjobs, f.listCronJobs)
	mux.HandleFunc("PUT "+cronjobs+"/{name}/status", f.putCronJobStatus)
	deployments := "/apis/" + job.APIVersion + "/deployments"
	mux.HandleFunc("GET "+deployments, f.listDeployments)
	mux.HandleFunc("PUT "+deployments+"/{name}/status", f.putDeploymentStatus)
	statefulsets := "/apis/" + job.APIVersion + "/statefulsets"
	mux.HandleFunc("GET "+statefulsets, f.listStatefulSets)
	mux.HandleFunc("PUT "+statefulsets+"/{name}/status", f.putStatefulSetStatus)
	volumes := "/apis/" + api.GroupVersion + "/volumes"
	mux.HandleFunc("GET "+volumes, f.listVolumes)
	mux.HandleFunc("PUT "+volumes+"/{name}", f.putVolume)
	mvms := "/apis/" + api.GroupVersion + "/microvms"
	mux.HandleFunc("GET "+mvms, f.listMicroVMs)
	mux.HandleFunc("PUT "+mvms+"/{name}", f.putMicroVM)
	mux.HandleFunc("DELETE "+mvms+"/{name}", f.deleteMicroVM)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) definitionsClient() *api.Client {
	return api.NewClient(f.server.URL+"/apis/"+api.APIExtensionsGroupVersion, nil)
}

func (f *fakeAPI) jobsClient() *api.CustomResourceClient {
	c, err := api.NewCustomResourceClient(f.server.URL, nil, job.Type)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fakeAPI) microvmsClient() *api.Client {
	return api.NewClient(f.server.URL+"/apis/"+api.GroupVersion, nil)
}

func (f *fakeAPI) rv() api.ResourceVersion {
	f.nextRV++
	return api.ResourceVersion(strconv.Itoa(f.nextRV))
}

// addJob stores a Job with the uid and the spec JSON given.
func (f *fakeAPI) addJob(name string, uid api.UID, spec string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[name] = &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.Kind},
		Metadata: api.ObjectMeta{Name: name, UID: uid, ResourceVersion: f.rv()},
		Spec:     json.RawMessage(spec),
	}
}

// addMicroVM stores a MicroVM named name, owned by the Job ref names, in phase.
func (f *fakeAPI) addMicroVM(name string, owner api.OwnerReference, phase api.MicroVMPhase) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.microvms[name] = &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{Name: name, ResourceVersion: f.rv(), OwnerReferences: []api.OwnerReference{owner}},
		Status:   api.MicroVMStatus{Phase: phase},
	}
}

// setPhase moves a stored MicroVM to phase, as the node agent does.
func (f *fakeAPI) setPhase(name string, phase api.MicroVMPhase) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.microvms[name].Status.Phase = phase
}

// microvmNames returns the names of the stored MicroVMs.
func (f *fakeAPI) microvmNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for n := range f.microvms {
		names = append(names, n)
	}
	return names
}

func (f *fakeAPI) microvm(name string) *api.MicroVM {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.microvms[name]
	if m == nil {
		return nil
	}
	c := *m
	return &c
}

// status returns the status the service wrote to the Job.
func (f *fakeAPI) status(name string) job.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s job.Status
	if raw := f.jobs[name].Status; len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			f.t.Fatal(err)
		}
	}
	return s
}

func (f *fakeAPI) definition(name string) *api.CustomResourceDefinition {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.definitions[name]
}

func (f *fakeAPI) getDefinition(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.definitions[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "definition"))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (f *fakeAPI) putDefinition(w http.ResponseWriter, r *http.Request) {
	var d api.CustomResourceDefinition
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.definitions[d.Metadata.Name]
	if old != nil && old.Metadata.ResourceVersion != d.Metadata.ResourceVersion {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "definition changed"))
		return
	}
	d.Metadata.ResourceVersion = f.rv()
	f.definitions[d.Metadata.Name] = &d
	f.definitionWrites++
	writeJSON(w, http.StatusOK, &d)
}

func (f *fakeAPI) listJobs(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	for _, o := range f.jobs {
		list.Items = append(list.Items, *o)
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) putJobStatus(w http.ResponseWriter, r *http.Request) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.jobs[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "job"))
		return
	}
	if o.Metadata.ResourceVersion != in.Metadata.ResourceVersion {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "job changed"))
		return
	}
	o.Status = in.Status
	o.Metadata.ResourceVersion = f.rv()
	f.statusWrites++
	writeJSON(w, http.StatusOK, o)
}

func (f *fakeAPI) listMicroVMs(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.MicroVMList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.MicroVM{}}
	for _, m := range f.microvms {
		list.Items = append(list.Items, *m)
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) putMicroVM(w http.ResponseWriter, r *http.Request) {
	var m api.MicroVM
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.invalidMicroVM:
		writeJSON(w, http.StatusUnprocessableEntity, api.NewAPIError(api.ReasonInvalid, "spec.resources is missing"))
	case f.microvms[m.Metadata.Name] != nil:
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonAlreadyExists, "microvm"))
	default:
		m.Metadata.ResourceVersion = f.rv()
		f.microvms[m.Metadata.Name] = &m
		f.checkBounds()
		writeJSON(w, http.StatusCreated, &m)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

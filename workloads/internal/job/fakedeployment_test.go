package job_test

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

func (f *fakeAPI) deploymentsClient() *api.CustomResourceClient {
	c, err := api.NewCustomResourceClient(f.server.URL, nil, job.DeploymentType)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// rolloutBounds is what a Deployment's replicas must satisfy after every write:
// no more than maxPresent MicroVMs, those being deleted included, and no fewer
// than minReady Ready and not being deleted. It records the extremes seen, so
// that a test shows the bounds were reached and not merely respected.
type rolloutBounds struct {
	owner      string
	maxPresent int
	minReady   int

	mostPresent int
	fewestReady int
}

// boundReplicas starts checking the replicas of the Deployment after every
// write, with the bounds given.
func (f *fakeAPI) boundReplicas(owner string, maxPresent, minReady int) *rolloutBounds {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bounds = &rolloutBounds{owner: owner, maxPresent: maxPresent, minReady: minReady, fewestReady: math.MaxInt}
	f.checkBounds()
	return f.bounds
}

// checkBounds reports a bound the stored MicroVMs break. The caller holds mu.
func (f *fakeAPI) checkBounds() {
	b := f.bounds
	if b == nil {
		return
	}
	present, ready := 0, 0
	for _, m := range f.microvms {
		if len(m.Metadata.OwnerReferences) == 0 || m.Metadata.OwnerReferences[0].Name != b.owner {
			continue
		}
		present++
		if m.Metadata.DeletionTimestamp == nil && isReady(m) {
			ready++
		}
	}
	b.mostPresent = max(b.mostPresent, present)
	b.fewestReady = min(b.fewestReady, ready)
	if present > b.maxPresent {
		f.t.Errorf("%d MicroVMs exist, above the bound %d", present, b.maxPresent)
	}
	if ready < b.minReady {
		f.t.Errorf("%d MicroVMs are Ready, below the bound %d", ready, b.minReady)
	}
}

func isReady(m *api.MicroVM) bool {
	return slices.ContainsFunc(m.Status.Conditions, func(c api.Condition) bool {
		return c.Type == "Ready" && c.Status == api.ConditionTrue
	})
}

// addDeployment stores a Deployment of generation 1 with the spec JSON given.
func (f *fakeAPI) addDeployment(name string, uid api.UID, spec string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployments[name] = &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.KindDeployment},
		Metadata: api.ObjectMeta{Name: name, UID: uid, Generation: 1, ResourceVersion: f.rv()},
		Spec:     json.RawMessage(spec),
	}
}

// setDeploymentSpec changes a stored Deployment's spec, as a user's update does.
func (f *fakeAPI) setDeploymentSpec(name, spec string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.deployments[name]
	d.Spec = json.RawMessage(spec)
	d.Metadata.Generation++
	d.Metadata.ResourceVersion = f.rv()
}

// deploymentStatus returns the status the service wrote to the Deployment.
func (f *fakeAPI) deploymentStatus(name string) job.DeploymentStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s job.DeploymentStatus
	if raw := f.deployments[name].Status; len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			f.t.Fatal(err)
		}
	}
	return s
}

// replicasOf returns copies of the MicroVMs the Deployment owns, by name.
func (f *fakeAPI) replicasOf(owner string) []api.MicroVM {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.MicroVM
	for _, m := range f.microvms {
		if len(m.Metadata.OwnerReferences) > 0 && m.Metadata.OwnerReferences[0].Name == owner {
			out = append(out, *m)
		}
	}
	slices.SortFunc(out, func(a, b api.MicroVM) int {
		return compareNames(a.Metadata.Name, b.Metadata.Name)
	})
	return out
}

func compareNames(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// addReplica stores a MicroVM owned by the Deployment, Ready or not, made from
// the template hash given.
func (f *fakeAPI) addReplica(name, owner string, uid api.UID, hash string, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{
			Name: name, ResourceVersion: f.rv(), Labels: map[string]string{job.TemplateHashLabel: hash},
			OwnerReferences: []api.OwnerReference{{APIVersion: job.APIVersion, Kind: job.KindDeployment, Name: owner, UID: uid, Controller: true}},
		},
	}
	if ready {
		markReady(m)
	}
	f.microvms[name] = m
}

func markReady(m *api.MicroVM) {
	now := time.Now()
	m.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, BindingEpoch: m.Spec.BindingEpoch,
		Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue, LastTransitionTime: &now}}}
}

// agentStep does what the node agents do in one round: every MicroVM not being
// deleted that is not Ready becomes Ready, and, when remove is set, every
// MicroVM being deleted is removed.
func (f *fakeAPI) agentStep(remove bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, m := range f.microvms {
		switch {
		case m.Metadata.DeletionTimestamp != nil && remove:
			delete(f.microvms, name)
		case m.Metadata.DeletionTimestamp == nil && !isReady(m):
			markReady(m)
			m.Metadata.ResourceVersion = f.rv()
		}
	}
	f.checkBounds()
}

// removeMicroVM removes a stored MicroVM at once, as a node agent does once it
// has released one being deleted.
func (f *fakeAPI) removeMicroVM(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.microvms, name)
	f.checkBounds()
}

// failMicroVM moves a stored MicroVM to Failed, as the node agent does.
func (f *fakeAPI) failMicroVM(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.microvms[name]
	m.Status = api.MicroVMStatus{Phase: api.MicroVMFailed, BindingEpoch: m.Spec.BindingEpoch}
	m.Metadata.ResourceVersion = f.rv()
}

// terminating returns the names of the stored MicroVMs that are being deleted.
func (f *fakeAPI) terminating() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for n, m := range f.microvms {
		if m.Metadata.DeletionTimestamp != nil {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

func (f *fakeAPI) listDeployments(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	for _, o := range f.deployments {
		list.Items = append(list.Items, *o)
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) putDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.deployments[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "deployment"))
		return
	}
	if o.Metadata.ResourceVersion != in.Metadata.ResourceVersion {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "deployment changed"))
		return
	}
	o.Status = in.Status
	o.Metadata.ResourceVersion = f.rv()
	writeJSON(w, http.StatusOK, o)
}

// deleteMicroVM sets the deletionTimestamp of a MicroVM, which stays stored
// until an agent step removes it.
func (f *fakeAPI) deleteMicroVM(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.microvms[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "microvm"))
		return
	}
	if string(m.Metadata.ResourceVersion) != r.URL.Query().Get("resourceVersion") {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "microvm changed"))
		return
	}
	now := time.Now()
	m.Metadata.DeletionTimestamp = &now
	m.Metadata.ResourceVersion = f.rv()
	f.checkBounds()
	writeJSON(w, http.StatusOK, m)
}

package scheduler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// fakeAPI serves the four endpoints the Scheduler calls over httptest, with
// the compare-and-swap the resource API specifies. A request to any other
// path fails the test, so a Lease read or a Machine PUT cannot go unseen.
type fakeAPI struct {
	t   *testing.T
	mu  sync.Mutex
	rev int

	machines map[string]*api.Machine
	nodes    map[string]*api.Node

	bindings      []api.Binding // every binding request, applied or not
	statusPuts    int
	bindingGate   chan struct{} // closed once gateArrivals bindings have arrived
	gateArrivals  int
	gateRemaining int
}

func newFakeAPI(t *testing.T) (*fakeAPI, *api.Client) {
	f := &fakeAPI{t: t, machines: map[string]*api.Machine{}, nodes: map[string]*api.Node{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /machines", f.listMachines)
	mux.HandleFunc("GET /nodes", f.listNodes)
	mux.HandleFunc("POST /machines/{name}/binding", f.bind)
	mux.HandleFunc("PUT /machines/{name}/status", f.putStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		writeError(w, api.NewAPIError(api.ReasonNotFound, "no such path"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return f, api.NewClient(ts.URL, ts.Client())
}

// holdBindings makes each binding request wait until n have arrived, so that
// n schedulers read the same version before any of them writes.
func (f *fakeAPI) holdBindings(n int) {
	f.bindingGate = make(chan struct{})
	f.gateRemaining = n
}

func (f *fakeAPI) addNode(n *api.Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
	n.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode}
	n.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.nodes[n.Metadata.Name] = n
}

func (f *fakeAPI) addMachine(m *api.Machine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
	m.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine}
	m.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	if m.Status.Phase == "" {
		m.Status.Phase = api.MachinePending
		m.Status.BindingEpoch = m.Spec.BindingEpoch
	}
	f.machines[m.Metadata.Name] = m
}

// machine returns a copy of the named Machine as stored, the zero Machine
// when none is.
func (f *fakeAPI) machine(name string) api.Machine {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.machines[name]; ok {
		return *m
	}
	return api.Machine{}
}

func (f *fakeAPI) bindingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bindings)
}

func (f *fakeAPI) statusPutCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusPuts
}

func (f *fakeAPI) listMachines(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.MachineList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachineList}, Metadata: api.ListMeta{ResourceVersion: api.ResourceVersion(strconv.Itoa(f.rev))}}
	for _, name := range sortedKeys(f.machines) {
		list.Items = append(list.Items, *f.machines[name])
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) listNodes(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.NodeList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNodeList}, Metadata: api.ListMeta{ResourceVersion: api.ResourceVersion(strconv.Itoa(f.rev))}}
	// The API promises no order; the Scheduler must not rely on one.
	for _, name := range slices.Backward(sortedKeys(f.nodes)) {
		list.Items = append(list.Items, *f.nodes[name])
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) bind(w http.ResponseWriter, r *http.Request) {
	var b api.Binding
	if !decode(w, r, &b) {
		return
	}
	f.mu.Lock()
	f.bindings = append(f.bindings, b)
	gate := f.bindingGate
	if gate != nil {
		f.gateArrivals++
		if f.gateArrivals == f.gateRemaining {
			close(gate)
		}
	}
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-time.After(5 * time.Second):
			f.t.Error("the bindings the test holds did not all arrive")
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.machines[r.PathValue("name")]
	switch {
	case !ok:
		writeError(w, api.NewAPIError(api.ReasonNotFound, "no machine"))
		return
	case m.Metadata.ResourceVersion != b.ResourceVersion:
		writeError(w, api.NewAPIError(api.ReasonConflict, "machine is at version %s, not %s", m.Metadata.ResourceVersion, b.ResourceVersion))
		return
	}
	next := *m
	if err := next.Bind(b); err != nil {
		writeAPIError(w, err)
		return
	}
	f.rev++
	next.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.machines[next.Metadata.Name] = &next
	writeJSON(w, http.StatusOK, next)
}

func (f *fakeAPI) putStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Machine
	if !decode(w, r, &next) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusPuts++
	m, ok := f.machines[r.PathValue("name")]
	switch {
	case !ok:
		writeError(w, api.NewAPIError(api.ReasonNotFound, "no machine"))
		return
	case m.Metadata.ResourceVersion != next.Metadata.ResourceVersion:
		writeError(w, api.NewAPIError(api.ReasonConflict, "machine is at version %s, not %s", m.Metadata.ResourceVersion, next.Metadata.ResourceVersion))
		return
	}
	if err := next.ValidateStatusUpdate(m); err != nil {
		writeAPIError(w, err)
		return
	}
	stored := *m
	stored.Status = next.Status
	f.rev++
	stored.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.machines[stored.Metadata.Name] = &stored
	writeJSON(w, http.StatusOK, stored)
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "%v", err))
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, e *api.APIError) {
	writeJSON(w, e.Code, e)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// node returns an Admitted, Ready Node with the capacity given.
func node(name, cpu, memory string) *api.Node {
	return &api.Node{
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.NodeSpec{Admitted: true},
		Status: api.NodeStatus{
			State:      api.NodeStateAdmitted,
			Capacity:   api.NodeCapacity{CPU: api.Quantity(cpu), Memory: api.Quantity(memory)},
			Conditions: []api.Condition{{Type: api.NodeReady, Status: api.ConditionTrue}},
		},
	}
}

// withCondition sets the condition of type t on n.
func withCondition(n *api.Node, t string, s api.ConditionStatus) *api.Node {
	n.Status.Conditions = slices.DeleteFunc(n.Status.Conditions, func(c api.Condition) bool { return c.Type == t })
	n.Status.Conditions = append(n.Status.Conditions, api.Condition{Type: t, Status: s})
	return n
}

// newMachine returns an unbound Machine requesting cpu and memory.
func newMachine(name, cpu, memory string) *api.Machine {
	return &api.Machine{
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.MachineSpec{
			RunStrategy: api.RunAlways,
			Resources:   api.ResourceRequirements{Requests: api.ResourceList{CPU: api.Quantity(cpu), Memory: api.Quantity(memory)}},
		},
	}
}

// bound returns m bound to nodeName at epoch.
func bound(m *api.Machine, nodeName string, epoch api.Epoch) *api.Machine {
	m.Spec.NodeName, m.Spec.BindingEpoch = nodeName, epoch
	m.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: epoch}
	return m
}

// writeAPIError answers with err, which the api package's validation returns
// as an *api.APIError.
func writeAPIError(w http.ResponseWriter, err error) {
	var e *api.APIError
	if !errors.As(err, &e) {
		e = api.NewAPIError(api.ReasonInvalid, "%v", err)
	}
	writeError(w, e)
}

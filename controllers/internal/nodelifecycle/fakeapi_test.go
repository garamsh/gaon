package nodelifecycle_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// fakeAPI serves only the endpoints the controller uses: listing and watching
// Leases, reading a Node and writing a Node's status. A request to any other
// path fails the test, which is how the tests show the controller never
// writes a Machine's binding or a Node's spec.
type fakeAPI struct {
	t      *testing.T
	client *api.Client

	mu        sync.Mutex
	leases    []api.Lease
	listRV    api.ResourceVersion
	nodes     map[string]*api.Node
	nextRV    int
	puts      int
	failPuts  int
	conflicts int
	lists     int

	// events feeds the open watch; a nil entry ends the stream.
	events chan *api.WatchEvent
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, nodes: map[string]*api.Node{}, events: make(chan *api.WatchEvent, 16), nextRV: 100}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /leases", f.listLeases)
	mux.HandleFunc("GET /nodes/{name}", f.getNode)
	mux.HandleFunc("PUT /nodes/{name}/status", f.putNodeStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.client = api.NewClient(srv.URL, nil)
	return f
}

func (f *fakeAPI) listLeases(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		f.watch(w, r)
		return
	}
	f.mu.Lock()
	f.lists++
	list := api.LeaseList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLeaseList},
		Metadata: api.ListMeta{ResourceVersion: f.listRV},
		Items:    append([]api.Lease{}, f.leases...),
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) watch(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case ev := <-f.events:
			if ev == nil {
				return
			}
			_ = enc.Encode(ev)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakeAPI) getNode(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "node"))
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (f *fakeAPI) putNodeStatus(w http.ResponseWriter, r *http.Request) {
	var in api.Node
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPuts > 0 {
		f.failPuts--
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	cur, ok := f.nodes[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "node"))
		return
	}
	if f.conflicts > 0 || cur.Metadata.ResourceVersion != in.Metadata.ResourceVersion {
		if f.conflicts > 0 {
			f.conflicts--
			f.bump(cur)
		}
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "node"))
		return
	}
	cur.Status = in.Status
	f.bump(cur)
	f.puts++
	writeJSON(w, http.StatusOK, cur)
}

// bump gives n a new resourceVersion, as any write to it would.
func (f *fakeAPI) bump(n *api.Node) {
	f.nextRV++
	n.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.nextRV))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeAPI) setLeases(rv api.ResourceVersion, leases ...api.Lease) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leases, f.listRV = leases, rv
}

func (f *fakeAPI) addNode(n *api.Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump(n)
	f.nodes[n.Metadata.Name] = n
}

// emit queues a watch event for the open watch.
func (f *fakeAPI) emit(typ api.EventType, l api.Lease) {
	ev, err := api.NewEvent(typ, &l)
	require.NoError(f.t, err)
	f.events <- &ev
}

func (f *fakeAPI) endWatch() { f.events <- nil }

func (f *fakeAPI) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeAPI) waitPuts(n int) {
	f.t.Helper()
	require.Eventually(f.t, func() bool { return f.putCount() >= n }, 5*time.Second, time.Millisecond, "the Node status was not written")
}

func (f *fakeAPI) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

// leaseExpired returns the stored LeaseExpired condition of the named Node.
func (f *fakeAPI) leaseExpired(name string) (api.Condition, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.nodes[name].Status.Conditions {
		if c.Type == api.NodeLeaseExpired {
			return c, true
		}
	}
	return api.Condition{}, false
}

// expiredIs asserts the named Node's LeaseExpired condition has status want.
func (f *fakeAPI) expiredIs(name string, want api.ConditionStatus) {
	f.t.Helper()
	c, ok := f.leaseExpired(name)
	require.True(f.t, ok, "the Node has no LeaseExpired condition")
	assert.Equal(f.t, want, c.Status)
}

// notExpired asserts the named Node does not have LeaseExpired true.
func (f *fakeAPI) notExpired(name string) {
	f.t.Helper()
	c, ok := f.leaseExpired(name)
	assert.False(f.t, ok && c.Status == api.ConditionTrue, "LeaseExpired is true")
}

func newNode(name string) *api.Node {
	return &api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.NodeSpec{Admitted: true},
		Status: api.NodeStatus{
			State:      api.NodeStateAdmitted,
			Conditions: []api.Condition{{Type: api.NodeReady, Status: api.ConditionTrue}},
		},
	}
}

// newLease returns a Lease of the given duration whose renewTime is far in the
// past: the controller must not read it.
func newLease(name string, rv api.ResourceVersion, seconds int32) api.Lease {
	return api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: name, ResourceVersion: rv},
		Spec: api.LeaseSpec{
			Holder:          "agent",
			RenewTime:       time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
			DurationSeconds: seconds,
		},
	}
}

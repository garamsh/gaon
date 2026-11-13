package endpoints_test

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

// fakeAPI serves only what the controller uses: listing and watching Services
// and MicroVMs, and reading and writing an Endpoints. A request to any other
// path, a DELETE of an Endpoints included, fails the test. A write is
// validated as the real server validates it and is conditioned on the
// resourceVersion.
type fakeAPI struct {
	t      *testing.T
	client *api.Client

	mu        sync.Mutex
	services  []api.Service
	microvms  []api.MicroVM
	endpoints map[string]*api.Endpoints
	nextRV    int
	puts      []api.Endpoints
	failPuts  int
	lists     int

	// svcEvents and mvmEvents feed the open watches; a nil entry ends the stream.
	svcEvents chan *api.WatchEvent
	mvmEvents chan *api.WatchEvent
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t: t, endpoints: map[string]*api.Endpoints{}, nextRV: 100,
		svcEvents: make(chan *api.WatchEvent, 16), mvmEvents: make(chan *api.WatchEvent, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", f.listServices)
	mux.HandleFunc("GET /microvms", f.listMicroVMs)
	mux.HandleFunc("GET /endpoints/{name}", f.getEndpoints)
	mux.HandleFunc("PUT /endpoints/{name}", f.putEndpoints)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.client = api.NewClient(srv.URL, nil)
	return f
}

func (f *fakeAPI) listServices(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		f.watch(w, r, f.svcEvents)
		return
	}
	f.mu.Lock()
	f.lists++
	list := api.ServiceList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: "ServiceList"},
		Metadata: api.ListMeta{ResourceVersion: "10"},
		Items:    append([]api.Service{}, f.services...),
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) listMicroVMs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		f.watch(w, r, f.mvmEvents)
		return
	}
	f.mu.Lock()
	list := api.MicroVMList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVMList},
		Metadata: api.ListMeta{ResourceVersion: "10"},
		Items:    append([]api.MicroVM{}, f.microvms...),
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) watch(w http.ResponseWriter, r *http.Request, events <-chan *api.WatchEvent) {
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case ev := <-events:
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

func (f *fakeAPI) getEndpoints(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.endpoints[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "endpoints"))
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (f *fakeAPI) putEndpoints(w http.ResponseWriter, r *http.Request) {
	var in api.Endpoints
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, in)
	if f.failPuts > 0 {
		f.failPuts--
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	if err := in.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.NewAPIError(api.ReasonInvalid, "%s", err.Error()))
		return
	}
	cur, ok := f.endpoints[r.PathValue("name")]
	switch {
	case !ok && in.Metadata.ResourceVersion != "":
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "endpoints"))
		return
	case ok && in.Metadata.ResourceVersion == "":
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonAlreadyExists, "endpoints"))
		return
	case ok && in.Metadata.ResourceVersion != cur.Metadata.ResourceVersion:
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "endpoints"))
		return
	}
	f.store(&in)
	writeJSON(w, http.StatusOK, &in)
}

// store keeps e under a new resourceVersion, as any write does.
func (f *fakeAPI) store(e *api.Endpoints) {
	f.nextRV++
	e.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.nextRV))
	f.endpoints[e.Metadata.Name] = e
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// seed sets what the first lists answer.
func (f *fakeAPI) seed(services []api.Service, microvms []api.MicroVM) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services, f.microvms = services, microvms
}

// seedEndpoints stores e as a user or an earlier run wrote it.
func (f *fakeAPI) seedEndpoints(e api.Endpoints) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store(&e)
}

func (f *fakeAPI) emitService(typ api.EventType, s api.Service) {
	ev, err := api.NewEvent(typ, &s)
	require.NoError(f.t, err)
	f.svcEvents <- &ev
}

func (f *fakeAPI) emitMicroVM(typ api.EventType, m api.MicroVM) {
	ev, err := api.NewEvent(typ, &m)
	require.NoError(f.t, err)
	f.mvmEvents <- &ev
}

func (f *fakeAPI) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

func (f *fakeAPI) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

func (f *fakeAPI) setFailPuts(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPuts = n
}

// stored returns the stored Endpoints of the named Service.
func (f *fakeAPI) stored(name string) (api.Endpoints, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.endpoints[name]
	if !ok {
		return api.Endpoints{}, false
	}
	return *e, true
}

// waitStored waits until the named Endpoints satisfies ok, and returns it.
func (f *fakeAPI) waitStored(name string, ok func(api.Endpoints) bool) api.Endpoints {
	f.t.Helper()
	var got api.Endpoints
	require.Eventually(f.t, func() bool {
		e, found := f.stored(name)
		got = e
		return found && ok(e)
	}, 5*time.Second, time.Millisecond, "the Endpoints did not reach the wanted state")
	return got
}

// ips returns the addresses of every subset of e, in order.
func ips(e api.Endpoints) []string {
	var out []string
	for _, s := range e.Subsets {
		for _, a := range s.Addresses {
			out = append(out, a.IP)
		}
	}
	return out
}

func assertNoPut(t *testing.T, f *fakeAPI, before int) {
	t.Helper()
	assert.Equal(t, before, f.putCount(), "the controller wrote an Endpoints")
}

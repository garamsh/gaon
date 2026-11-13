package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/garamsh/gaon/lib/api"
)

// fakeAPI serves the list and the watch of Services and Endpoints under the
// token it holds, and can stop answering.
type fakeAPI struct {
	*httptest.Server

	mu        sync.Mutex
	services  []api.Service
	endpoints []api.Endpoints
	token     string
	down      bool
	gate      chan struct{}

	events       map[string]chan api.WatchEvent
	refused      atomic.Int32
	unauthorized atomic.Int32
}

func newFakeAPI(t *testing.T, token string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		token: token,
		events: map[string]chan api.WatchEvent{
			"services":  make(chan api.WatchEvent, 16),
			"endpoints": make(chan api.WatchEvent, 16),
		},
	}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down, token, gate := f.down, f.token, f.gate
	f.mu.Unlock()
	if down {
		f.refused.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		f.unauthorized.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kind := path.Base(r.URL.Path)
	if r.URL.Query().Get("watch") == "true" {
		f.watch(w, r, kind)
		return
	}
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch kind {
	case "services":
		_ = json.NewEncoder(w).Encode(api.ServiceList{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceList},
			Metadata: api.ListMeta{ResourceVersion: "1"},
			Items:    f.services,
		})
	case "endpoints":
		_ = json.NewEncoder(w).Encode(api.EndpointsList{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpointsList},
			Metadata: api.ListMeta{ResourceVersion: "1"},
			Items:    f.endpoints,
		})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) watch(w http.ResponseWriter, r *http.Request, kind string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case ev := <-f.events[kind]:
			if err := enc.Encode(ev); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// setLists replaces what a list returns.
func (f *fakeAPI) setLists(services []api.Service, endpoints []api.Endpoints) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services, f.endpoints = services, endpoints
}

// holdLists makes a list wait until the returned function is called.
func (f *fakeAPI) holdLists() (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	return func() { close(gate) }
}

// setDown makes every request fail and ends the watches in progress.
func (f *fakeAPI) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
	if down {
		f.CloseClientConnections()
	}
}

// setToken changes the token requests must carry and ends the watches in
// progress, so the unit lists again.
func (f *fakeAPI) setToken(token string) {
	f.mu.Lock()
	f.token = token
	f.mu.Unlock()
	f.CloseClientConnections()
}

// pushService sends a watch event for s.
func (f *fakeAPI) pushService(t *testing.T, typ api.EventType, s api.Service) {
	t.Helper()
	ev, err := api.NewEvent(typ, &s)
	if err != nil {
		t.Fatal(err)
	}
	f.events["services"] <- ev
}

// pushEndpoints sends a watch event for e.
func (f *fakeAPI) pushEndpoints(t *testing.T, typ api.EventType, e api.Endpoints) {
	t.Helper()
	ev, err := api.NewEvent(typ, &e)
	if err != nil {
		t.Fatal(err)
	}
	f.events["endpoints"] <- ev
}

func service(name, clusterIP string) api.Service {
	return api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.ServiceSpec{ClusterIP: clusterIP},
	}
}

// endpointsOf returns the Endpoints of name with one address for each pair of
// a MicroVM name and an IP.
func endpointsOf(name string, microVMs map[string]string) api.Endpoints {
	e := api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: name},
	}
	if len(microVMs) == 0 {
		return e
	}
	subset := api.EndpointSubset{Ports: []api.EndpointPort{{Port: 80, Protocol: "TCP"}}}
	for vm, ip := range microVMs {
		subset.Addresses = append(subset.Addresses, api.EndpointAddress{
			IP:        ip,
			TargetRef: &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: vm},
		})
	}
	e.Subsets = []api.EndpointSubset{subset}
	return e
}

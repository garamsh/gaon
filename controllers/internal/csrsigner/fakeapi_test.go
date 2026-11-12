package csrsigner_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// fakeAPI serves only the endpoints the signer uses: listing and watching
// CertificateSigningRequests, reading one and writing its status. A request to
// any other path fails the test.
type fakeAPI struct {
	t      *testing.T
	client *api.Client

	mu        sync.Mutex
	requests  map[string]*api.CertificateSigningRequest
	order     []string
	nextRV    int
	puts      []string
	failPuts  int
	conflicts int
	// beforeConflict runs when a write is about to be answered with a conflict.
	beforeConflict func(r *api.CertificateSigningRequest)

	// events feeds the open watch; a nil entry ends the stream.
	events chan *api.WatchEvent
	// watching is closed when the watch opens.
	watching chan struct{}
	once     sync.Once
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, requests: map[string]*api.CertificateSigningRequest{}, events: make(chan *api.WatchEvent, 16), watching: make(chan struct{}), nextRV: 100}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /certificatesigningrequests", f.list)
	mux.HandleFunc("GET /certificatesigningrequests/{name}", f.get)
	mux.HandleFunc("PUT /certificatesigningrequests/{name}/status", f.putStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.client = api.NewClient(srv.URL, nil)
	return f
}

// list lists every stored request in the order they were added, and serves
// the watch.
func (f *fakeAPI) list(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		f.once.Do(func() { close(f.watching) })
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
	f.mu.Lock()
	list := api.CertificateSigningRequestList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequestList},
		Metadata: api.ListMeta{ResourceVersion: "10"},
	}
	for _, name := range f.order {
		list.Items = append(list.Items, *f.requests[name])
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.requests[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "certificatesigningrequest"))
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (f *fakeAPI) putStatus(w http.ResponseWriter, r *http.Request) {
	var in api.CertificateSigningRequest
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
	if err := in.ValidateStatus(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.NewAPIError(api.ReasonInvalid, "%s", err.Error()))
		return
	}
	name := r.PathValue("name")
	cur, ok := f.requests[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "certificatesigningrequest"))
		return
	}
	if f.conflicts > 0 || cur.Metadata.ResourceVersion != in.Metadata.ResourceVersion {
		if f.conflicts > 0 {
			f.conflicts--
			if f.beforeConflict != nil {
				f.beforeConflict(cur)
			}
			f.bump(cur)
		}
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "certificatesigningrequest"))
		return
	}
	cur.Status = in.Status
	f.bump(cur)
	f.puts = append(f.puts, name)
	writeJSON(w, http.StatusOK, cur)
}

// bump gives r a new resourceVersion, as any write to it would.
func (f *fakeAPI) bump(r *api.CertificateSigningRequest) {
	f.nextRV++
	r.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.nextRV))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// add stores r, as it is listed after the API server created it.
func (f *fakeAPI) add(r *api.CertificateSigningRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump(r)
	f.requests[r.Metadata.Name] = r
	f.order = append(f.order, r.Metadata.Name)
}

// emit queues a watch event for the open watch.
func (f *fakeAPI) emit(typ api.EventType, r *api.CertificateSigningRequest) {
	ev, err := api.NewEvent(typ, r)
	require.NoError(f.t, err)
	f.events <- &ev
}

func (f *fakeAPI) endWatch() { f.events <- nil }

// stored returns a copy of the request the API holds under name.
func (f *fakeAPI) stored(name string) api.CertificateSigningRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.requests[name]
}

// written returns the names of the requests whose status was written, in order.
func (f *fakeAPI) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.puts...)
}

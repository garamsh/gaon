package command_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// listKinds names the list kind of each collection the fake serves.
var listKinds = map[string]string{
	"microvms": api.KindMicroVMList,
	"nodes":    api.KindNodeList,
	"leases":   api.KindLeaseList,
	"volumes":  "VolumeList",

	"secrets":         api.KindSecretList,
	"configmaps":      api.KindConfigMapList,
	"serviceaccounts": api.KindServiceAccountList,
	"services":        "ServiceList",
	"endpoints":       "EndpointsList",
	"networkpolicies": "NetworkPolicyList",

	"clusterroles":               api.KindClusterRoleList,
	"clusterrolebindings":        api.KindClusterRoleBindingList,
	"customresourcedefinitions":  api.KindCustomResourceDefinitionList,
	"certificatesigningrequests": api.KindCertificateSigningRequestList,
	"widgets":                    "WidgetList",
	"gadgets":                    "GadgetList",
	"things":                     "ThingList",
}

// computePrefix is where the fake serves the compute group, which --server
// names. Its paths are kept and recorded without it; the paths of every other
// group are kept and recorded in full.
const computePrefix = "/apis/" + api.GroupVersion

// fakeServer is an in-memory resource API: objects by path, one revision
// counter, and a compare-and-swap on resourceVersion for every write.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	rev      int
	objects  map[string]map[string]any
	requests []string
	// beforeWrite runs after a PUT or DELETE arrives and before the server
	// decides on it, with the lock held, so a test can change the store as a
	// concurrent client would.
	beforeWrite func(method, path string)
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, objects: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) url() string { return f.srv.URL }

// seed stores obj at path as a client's create would, and returns its version.
func (f *fakeServer) seed(path string, obj any) api.ResourceVersion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store(path, toMap(f.t, obj))
}

// load reads the object stored at path into a T.
func load[T any](f *fakeServer, path string) T {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[path]
	require.True(f.t, ok, "nothing stored at %s", path)
	var out T
	require.NoError(f.t, json.Unmarshal(mustMarshal(f.t, obj), &out))
	return out
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// writes counts the requests that are not reads.
func (f *fakeServer) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if !strings.HasPrefix(r, http.MethodGet+" ") {
			n++
		}
	}
	return n
}

func (f *fakeServer) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeServer) store(path string, obj map[string]any) api.ResourceVersion {
	f.rev++
	rv := strconv.Itoa(f.rev)
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		meta = map[string]any{}
		obj["metadata"] = meta
	}
	meta["resourceVersion"] = rv
	f.objects[path] = obj
	return api.ResourceVersion(rv)
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// path is the key the object is stored under, and groupVersion the one
	// its list reports.
	// A path outside /apis is the compute group served at the root, as for a
	// --server that does not end in computePrefix.
	path, groupVersion := r.URL.Path, api.GroupVersion
	rest := path
	if p, ok := strings.CutPrefix(path, computePrefix); ok {
		path, rest = p, p
	} else if parts := strings.SplitN(strings.Trim(path, "/"), "/", 4); len(parts) == 4 && parts[0] == "apis" {
		rest, groupVersion = "/"+parts[3], parts[1]+"/"+parts[2]
	}
	f.requests = append(f.requests, r.Method+" "+path)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	collection := parts[0]
	if _, ok := listKinds[collection]; !ok || len(parts) > 2 {
		fail(w, api.ReasonNotFound, "no such path")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		f.list(w, path, groupVersion, collection)
		return
	}
	if len(parts) != 2 {
		fail(w, api.ReasonNotFound, "no such path")
		return
	}
	switch r.Method {
	case http.MethodGet:
		obj, ok := f.objects[path]
		if !ok {
			fail(w, api.ReasonNotFound, "not found")
			return
		}
		reply(w, obj)
	case http.MethodPut:
		if f.beforeWrite != nil {
			f.beforeWrite(r.Method, path)
		}
		var obj map[string]any
		if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
			fail(w, api.ReasonInvalid, err.Error())
			return
		}
		f.put(w, path, obj)
	case http.MethodDelete:
		if f.beforeWrite != nil {
			f.beforeWrite(r.Method, path)
		}
		f.remove(w, path, r.URL.Query().Get("resourceVersion"))
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (f *fakeServer) put(w http.ResponseWriter, path string, obj map[string]any) {
	var sent string
	if meta, ok := obj["metadata"].(map[string]any); ok {
		sent, _ = meta["resourceVersion"].(string)
	}
	stored, exists := f.objects[path]
	switch {
	case !exists && sent != "":
		fail(w, api.ReasonNotFound, "not found")
		return
	case exists && sent == "":
		fail(w, api.ReasonAlreadyExists, "already exists")
		return
	case exists && sent != versionOf(stored):
		fail(w, api.ReasonConflict, "resourceVersion "+sent+" is not the current "+versionOf(stored))
		return
	}
	f.store(path, obj)
	reply(w, f.objects[path])
}

func (f *fakeServer) remove(w http.ResponseWriter, path, rv string) {
	stored, ok := f.objects[path]
	switch {
	case !ok:
		fail(w, api.ReasonNotFound, "not found")
		return
	case rv != versionOf(stored):
		fail(w, api.ReasonConflict, "resourceVersion "+rv+" is not the current "+versionOf(stored))
		return
	}
	meta, ok := stored["metadata"].(map[string]any)
	if !ok {
		fail(w, api.ReasonInvalid, "stored object has no metadata")
		return
	}
	meta["deletionTimestamp"] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if fin, _ := meta["finalizers"].([]any); len(fin) == 0 {
		// Nothing holds the object, so the server removes it at once.
		delete(f.objects, path)
		reply(w, stored)
		return
	}
	f.store(path, stored)
	reply(w, f.objects[path])
}

func (f *fakeServer) list(w http.ResponseWriter, collectionPath, groupVersion, collection string) {
	var paths []string
	for p := range f.objects {
		if strings.HasPrefix(p, collectionPath+"/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	items := make([]any, 0, len(paths))
	for _, p := range paths {
		items = append(items, f.objects[p])
	}
	reply(w, map[string]any{
		"apiVersion": groupVersion,
		"kind":       listKinds[collection],
		"metadata":   map[string]any{"resourceVersion": strconv.Itoa(f.rev)},
		"items":      items,
	})
}

func versionOf(obj map[string]any) string {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	rv, _ := meta["resourceVersion"].(string)
	return rv
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, reason api.Reason, message string) {
	e := api.NewAPIError(reason, "%s", message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Code)
	_ = json.NewEncoder(w).Encode(e)
}

func mustMarshal(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func toMap(t *testing.T, v any) map[string]any {
	var m map[string]any
	require.NoError(t, json.Unmarshal(mustMarshal(t, v), &m))
	return m
}

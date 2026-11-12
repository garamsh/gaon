package garbagecollector_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/controllers/internal/garbagecollector"
	"github.com/garamsh/gaon/lib/api"
)

// The kinds the tests use.
var (
	mvmType    = api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindMicroVM, Plural: "microvms"}
	volumeType = api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindVolume, Plural: "volumes"}
	nodeType   = api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindNode, Plural: "nodes"}
	widgetType = api.CustomResourceType{Group: "apps.gaon.garam.sh", Version: "v1alpha1", Kind: "Widget", Plural: "widgets"}
	gizmoType  = api.CustomResourceType{Group: "apps.gaon.garam.sh", Version: "v1alpha1", Kind: "Gizmo", Plural: "gizmos"}
)

const definitionsPrefix = "/apis/" + api.APIExtensionsGroupVersion + "/customresourcedefinitions"

func prefix(t api.CustomResourceType) string {
	return "/apis/" + t.Group + "/" + t.Version + "/" + t.Plural
}

func apiVersion(t api.CustomResourceType) string { return t.Group + "/" + t.Version }

// ownerOf is a reference to the object of kind t named name.
func ownerOf(t api.CustomResourceType, name string, uid api.UID) api.OwnerReference {
	return api.OwnerReference{APIVersion: apiVersion(t), Kind: t.Kind, Name: name, UID: uid}
}

// fakeAPI serves list, get and delete of any kind under /apis/<group>/<version>
// and the definitions list, over objects the test adds. A kind with none
// answers an empty list. A delete removes the object, as a server does for an
// object with no finalizer.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu      sync.Mutex
	objects map[string]map[string]*api.CustomObject
	hidden  map[string]bool
	failing map[string]bool
	after   map[string]func()
	deletes []string
	nextRV  int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t: t, objects: map[string]map[string]*api.CustomObject{}, hidden: map[string]bool{},
		failing: map[string]bool{}, after: map[string]func(){}, nextRV: 100,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apis/{group}/{version}/{plural}", f.list)
	mux.HandleFunc("GET /apis/{group}/{version}/{plural}/{name}", f.get)
	mux.HandleFunc("DELETE /apis/{group}/{version}/{plural}/{name}", f.remove)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// clientFor is the garbagecollector.ClientFor the composition site would pass.
func (f *fakeAPI) clientFor() garbagecollector.ClientFor {
	return func(t api.CustomResourceType) (*api.CustomResourceClient, error) {
		return api.NewCustomResourceClient(f.server.URL, nil, t)
	}
}

func (f *fakeAPI) definitions() *api.Client {
	return api.NewClient(f.server.URL+"/apis/"+api.APIExtensionsGroupVersion, nil)
}

// add stores an object of kind t, with the owners given, and returns it.
func (f *fakeAPI) add(t api.CustomResourceType, name string, uid api.UID, owners ...api.OwnerReference) *api.CustomObject {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRV++
	o := &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: apiVersion(t), Kind: t.Kind},
		Metadata: api.ObjectMeta{Name: name, UID: uid, ResourceVersion: api.ResourceVersion(strconv.Itoa(f.nextRV)), OwnerReferences: owners},
	}
	f.store(prefix(t), o)
	return o
}

func (f *fakeAPI) store(p string, o *api.CustomObject) {
	if f.objects[p] == nil {
		f.objects[p] = map[string]*api.CustomObject{}
	}
	f.objects[p][o.Metadata.Name] = o
}

// register adds a definition of the kind t, with the Established condition
// set to established.
func (f *fakeAPI) register(t api.CustomResourceType, established bool) {
	status := api.ConditionFalse
	if established {
		status = api.ConditionTrue
	}
	def := api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: t.Plural + "." + t.Group},
		Spec: api.CustomResourceDefinitionSpec{
			Group: t.Group, Scope: api.ScopeCluster,
			Names:    api.CustomResourceNames{Kind: t.Kind, Plural: t.Plural},
			Versions: []api.CustomResourceVersion{{Name: t.Version, Served: true, Storage: true}},
		},
		Status: api.CustomResourceDefinitionStatus{Conditions: []api.Condition{{Type: api.CRDEstablished, Status: status}}},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		f.t.Fatal(err)
	}
	var o api.CustomObject
	if err := json.Unmarshal(raw, &o); err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store(definitionsPrefix, &o)
}

// has reports whether the object is stored.
func (f *fakeAPI) has(t api.CustomResourceType, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[prefix(t)][name]
	return ok
}

// hide keeps the object out of lists of its kind, while a read still finds it.
func (f *fakeAPI) hide(t api.CustomResourceType, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidden[prefix(t)+"/"+name] = true
}

// failList makes a list of the kind answer 500.
func (f *fakeAPI) failList(t api.CustomResourceType) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing[prefix(t)] = true
}

// afterList runs fn once a list of the kind t has been read from the store and
// before it is answered, outside the fake's lock.
func (f *fakeAPI) afterList(t api.CustomResourceType, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.after[prefix(t)] = fn
}

// touch changes an object's resourceVersion, as a writer does.
func (f *fakeAPI) touch(t api.CustomResourceType, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRV++
	f.objects[prefix(t)][name].Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.nextRV))
}

// deleted returns the objects deleted so far as <Kind>/<name>, sorted.
func (f *fakeAPI) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.deletes)
	slices.Sort(out)
	return out
}

func (f *fakeAPI) list(w http.ResponseWriter, r *http.Request) {
	p := "/apis/" + r.PathValue("group") + "/" + r.PathValue("version") + "/" + r.PathValue("plural")
	f.mu.Lock()
	objs := f.objects[p]
	failing := f.failing[p]
	hook := f.after[p]
	list := api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	for name, o := range objs {
		if !f.hidden[p+"/"+name] {
			list.Items = append(list.Items, *o)
		}
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if failing {
		writeJSON(w, http.StatusInternalServerError, api.NewAPIError(api.ReasonInternalError, "list"))
	} else {
		writeJSON(w, http.StatusOK, list)
	}
}

func (f *fakeAPI) get(w http.ResponseWriter, r *http.Request) {
	p := "/apis/" + r.PathValue("group") + "/" + r.PathValue("version") + "/" + r.PathValue("plural")
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[p][r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "object"))
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (f *fakeAPI) remove(w http.ResponseWriter, r *http.Request) {
	p := "/apis/" + r.PathValue("group") + "/" + r.PathValue("version") + "/" + r.PathValue("plural")
	name := r.PathValue("name")
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[p][name]
	switch {
	case !ok:
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "object"))
	case string(o.Metadata.ResourceVersion) != r.URL.Query().Get("resourceVersion"):
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "object changed"))
	default:
		delete(f.objects[p], name)
		f.deletes = append(f.deletes, o.Kind+"/"+name)
		writeJSON(w, http.StatusOK, o)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fakeClock hands out the channel of each After call, so a test learns when a
// sweep has finished and can start the next.
type fakeClock struct {
	waits chan chan time.Time
	// pending starts the sweep after the one the test last waited for.
	pending chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{waits: make(chan chan time.Time, 16)} }

func (c *fakeClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.waits <- ch
	return ch
}

// sweepDone waits until the collector has finished a sweep and is waiting for
// the next, and returns the channel that starts it.
func (c *fakeClock) sweepDone(t *testing.T) chan time.Time {
	t.Helper()
	select {
	case ch := <-c.waits:
		return ch
	case <-time.After(5 * time.Second):
		t.Fatal("the collector did not finish a sweep")
		return nil
	}
}

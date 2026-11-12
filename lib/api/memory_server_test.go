package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// memoryServer is an in-memory resource API server for the client tests: the
// paths of resource-api.md §Paths over a map, one revision counter for the
// whole store, and a watch log that compact trims.
type memoryServer struct {
	mu        sync.Mutex
	rev       int64
	objects   map[string][]byte
	log       []loggedEvent
	compacted int64
	changed   chan struct{}
	url       string
}

type loggedEvent struct {
	rev   int64
	kind  string
	event api.WatchEvent
}

func newMemoryServer(t *testing.T) (*memoryServer, *api.Client) {
	t.Helper()
	s := &memoryServer{objects: map[string][]byte{}, changed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /microvms/{name}", s.putMicroVM)
	mux.HandleFunc("POST /microvms/{name}/binding", s.bindMicroVM)
	mux.HandleFunc("PUT /microvms/{name}/status", s.putMicroVMStatus)
	mux.HandleFunc("DELETE /microvms/{name}", s.deleteMicroVM)
	mux.HandleFunc("GET /microvms/{name}", getObject[api.MicroVM](s, "microvms/"))
	mux.HandleFunc("GET /microvms", listOrWatch(s, "microvms/", api.KindMicroVM, func(items []api.MicroVM, rv api.ResourceVersion) any {
		return &api.MicroVMList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVMList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /volumes/{name}", putValidated(s, "volumes/", api.KindVolume, func(v *api.Volume) (*api.ObjectMeta, error) { return &v.Metadata, v.Validate() }))
	mux.HandleFunc("DELETE /volumes/{name}", s.deleteVolume)
	mux.HandleFunc("GET /volumes/{name}", getObject[api.Volume](s, "volumes/"))
	mux.HandleFunc("GET /volumes", listOrWatch(s, "volumes/", api.KindVolume, func(items []api.Volume, rv api.ResourceVersion) any {
		return &api.VolumeList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolumeList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /volumes/{name}/status", s.putVolumeStatus)
	mux.HandleFunc("PUT /nodes/{name}", s.putNode)
	mux.HandleFunc("PUT /nodes/{name}/status", s.putNodeStatus)
	mux.HandleFunc("GET /nodes/{name}", getObject[api.Node](s, "nodes/"))
	mux.HandleFunc("GET /nodes", listOrWatch(s, "nodes/", api.KindNode, func(items []api.Node, rv api.ResourceVersion) any {
		return &api.NodeList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNodeList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /leases/{name}", s.putLease)
	mux.HandleFunc("GET /leases/{name}", getObject[api.Lease](s, "leases/"))
	mux.HandleFunc("GET /leases", listOrWatch(s, "leases/", api.KindLease, func(items []api.Lease, rv api.ResourceVersion) any {
		return &api.LeaseList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLeaseList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /certificatesigningrequests/{name}", s.putCertificateSigningRequest)
	mux.HandleFunc("PUT /certificatesigningrequests/{name}/status", s.putCertificateSigningRequestStatus)
	mux.HandleFunc("GET /certificatesigningrequests/{name}", getObject[api.CertificateSigningRequest](s, "certificatesigningrequests/"))
	mux.HandleFunc("GET /certificatesigningrequests", listOrWatch(s, "certificatesigningrequests/", api.KindCertificateSigningRequest, func(items []api.CertificateSigningRequest, rv api.ResourceVersion) any {
		return &api.CertificateSigningRequestList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequestList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /clusterroles/{name}", putValidated(s, "clusterroles/", api.KindClusterRole, func(r *api.ClusterRole) (*api.ObjectMeta, error) { return &r.Metadata, r.Validate() }))
	mux.HandleFunc("DELETE /clusterroles/{name}", deleteObject[api.ClusterRole](s, "clusterroles/", api.KindClusterRole, func(r *api.ClusterRole) *api.ObjectMeta { return &r.Metadata }))
	mux.HandleFunc("GET /clusterroles/{name}", getObject[api.ClusterRole](s, "clusterroles/"))
	mux.HandleFunc("GET /clusterroles", listOrWatch(s, "clusterroles/", api.KindClusterRole, func(items []api.ClusterRole, rv api.ResourceVersion) any {
		return &api.ClusterRoleList{TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /clusterrolebindings/{name}", putValidated(s, "clusterrolebindings/", api.KindClusterRoleBinding, func(b *api.ClusterRoleBinding) (*api.ObjectMeta, error) { return &b.Metadata, b.Validate() }))
	mux.HandleFunc("DELETE /clusterrolebindings/{name}", deleteObject[api.ClusterRoleBinding](s, "clusterrolebindings/", api.KindClusterRoleBinding, func(b *api.ClusterRoleBinding) *api.ObjectMeta { return &b.Metadata }))
	mux.HandleFunc("GET /clusterrolebindings/{name}", getObject[api.ClusterRoleBinding](s, "clusterrolebindings/"))
	mux.HandleFunc("GET /clusterrolebindings", listOrWatch(s, "clusterrolebindings/", api.KindClusterRoleBinding, func(items []api.ClusterRoleBinding, rv api.ResourceVersion) any {
		return &api.ClusterRoleBindingList{TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBindingList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /customresourcedefinitions/{name}", putValidated(s, "customresourcedefinitions/", api.KindCustomResourceDefinition, func(d *api.CustomResourceDefinition) (*api.ObjectMeta, error) { return &d.Metadata, d.Validate() }))
	mux.HandleFunc("DELETE /customresourcedefinitions/{name}", deleteObject[api.CustomResourceDefinition](s, "customresourcedefinitions/", api.KindCustomResourceDefinition, func(d *api.CustomResourceDefinition) *api.ObjectMeta { return &d.Metadata }))
	mux.HandleFunc("GET /customresourcedefinitions/{name}", getObject[api.CustomResourceDefinition](s, "customresourcedefinitions/"))
	mux.HandleFunc("GET /customresourcedefinitions", listOrWatch(s, "customresourcedefinitions/", api.KindCustomResourceDefinition, func(items []api.CustomResourceDefinition, rv api.ResourceVersion) any {
		return &api.CustomResourceDefinitionList{TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinitionList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /mutatingwebhookconfigurations/{name}", putValidated(s, "mutatingwebhookconfigurations/", api.KindMutatingWebhookConfiguration, func(c *api.MutatingWebhookConfiguration) (*api.ObjectMeta, error) { return &c.Metadata, c.Validate() }))
	mux.HandleFunc("DELETE /mutatingwebhookconfigurations/{name}", deleteObject[api.MutatingWebhookConfiguration](s, "mutatingwebhookconfigurations/", api.KindMutatingWebhookConfiguration, func(c *api.MutatingWebhookConfiguration) *api.ObjectMeta { return &c.Metadata }))
	mux.HandleFunc("GET /mutatingwebhookconfigurations/{name}", getObject[api.MutatingWebhookConfiguration](s, "mutatingwebhookconfigurations/"))
	mux.HandleFunc("GET /mutatingwebhookconfigurations", listOrWatch(s, "mutatingwebhookconfigurations/", api.KindMutatingWebhookConfiguration, func(items []api.MutatingWebhookConfiguration, rv api.ResourceVersion) any {
		return &api.MutatingWebhookConfigurationList{TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfigurationList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	mux.HandleFunc("PUT /validatingwebhookconfigurations/{name}", putValidated(s, "validatingwebhookconfigurations/", api.KindValidatingWebhookConfiguration, func(c *api.ValidatingWebhookConfiguration) (*api.ObjectMeta, error) { return &c.Metadata, c.Validate() }))
	mux.HandleFunc("DELETE /validatingwebhookconfigurations/{name}", deleteObject[api.ValidatingWebhookConfiguration](s, "validatingwebhookconfigurations/", api.KindValidatingWebhookConfiguration, func(c *api.ValidatingWebhookConfiguration) *api.ObjectMeta { return &c.Metadata }))
	mux.HandleFunc("GET /validatingwebhookconfigurations/{name}", getObject[api.ValidatingWebhookConfiguration](s, "validatingwebhookconfigurations/"))
	mux.HandleFunc("GET /validatingwebhookconfigurations", listOrWatch(s, "validatingwebhookconfigurations/", api.KindValidatingWebhookConfiguration, func(items []api.ValidatingWebhookConfiguration, rv api.ResourceVersion) any {
		return &api.ValidatingWebhookConfigurationList{TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfigurationList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
	for _, ct := range []api.CustomResourceType{widgetType, gadgetType} {
		s.customRoutes(mux, ct)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.url = ts.URL
	return s, api.NewClient(ts.URL, ts.Client())
}

// customRoutes serves the objects of a registered kind under its own root, as
// the API server serves them once its definition is established.
func (s *memoryServer) customRoutes(mux *http.ServeMux, ct api.CustomResourceType) {
	root := "/apis/" + ct.Group + "/" + ct.Version + "/" + ct.Plural
	prefix := ct.Plural + "/"
	mux.HandleFunc("PUT "+root+"/{name}", putValidated(s, prefix, ct.Kind, func(o *api.CustomObject) (*api.ObjectMeta, error) { return &o.Metadata, o.Validate(ct) }))
	mux.HandleFunc("PUT "+root+"/{name}/status", func(w http.ResponseWriter, r *http.Request) {
		var next api.CustomObject
		if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		o, found := load[api.CustomObject](s, prefix+next.Metadata.Name)
		if err := s.requireVersion(found, o.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
			writeError(w, err)
			return
		}
		o.Status = next.Status
		store(s, w, prefix+o.Metadata.Name, ct.Kind, &o.Metadata, &o, api.EventModified)
	})
	mux.HandleFunc("DELETE "+root+"/{name}", deleteObject[api.CustomObject](s, prefix, ct.Kind, func(o *api.CustomObject) *api.ObjectMeta { return &o.Metadata }))
	mux.HandleFunc("GET "+root+"/{name}", getObject[api.CustomObject](s, prefix))
	mux.HandleFunc("GET "+root, listOrWatch(s, prefix, ct.Kind, func(items []api.CustomObject, rv api.ResourceVersion) any {
		return &api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: ct.Group + "/" + ct.Version, Kind: ct.Kind + "List"}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}
	}))
}

// memoryRequester is the username the memory server, which authenticates no
// one, sets on every request it creates.
const memoryRequester = "memory-requester"

// putCertificateSigningRequest creates a request: its spec is immutable, so a
// version is refused, and the username is the server's, not the caller's.
func (s *memoryServer) putCertificateSigningRequest(w http.ResponseWriter, r *http.Request) {
	var csr api.CertificateSigningRequest
	if !decodeBody(w, r, &csr) || !s.nameMatches(w, r, csr.Metadata.Name) {
		return
	}
	if err := csr.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if csr.Metadata.ResourceVersion != "" {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "spec of %q is immutable", csr.Metadata.Name))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "certificatesigningrequests/" + csr.Metadata.Name
	_, found := load[api.CertificateSigningRequest](s, key)
	if err := s.checkVersion(found, "", "", csr.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	csr.Spec.Username = memoryRequester
	csr.Status = api.CertificateSigningRequestStatus{}
	store(s, w, key, api.KindCertificateSigningRequest, &csr.Metadata, &csr, api.EventAdded)
}

func (s *memoryServer) putCertificateSigningRequestStatus(w http.ResponseWriter, r *http.Request) {
	var next api.CertificateSigningRequest
	if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "certificatesigningrequests/" + next.Metadata.Name
	csr, found := load[api.CertificateSigningRequest](s, key)
	if err := s.requireVersion(found, csr.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatus(); err != nil {
		writeError(w, err)
		return
	}
	csr.Status = next.Status
	store(s, w, key, api.KindCertificateSigningRequest, &csr.Metadata, &csr, api.EventModified)
}

// compact drops the watch log, so a watch from an earlier version expires.
func (s *memoryServer) compact() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compacted = s.rev
	s.log = nil
}

// seedVolume stores v, which has no create path in the contract.
func (s *memoryServer) seedVolume(v *api.Volume) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects["volumes/"+v.Metadata.Name] = mustJSON(v)
}

func (s *memoryServer) putMicroVM(w http.ResponseWriter, r *http.Request) {
	var m api.MicroVM
	if !decodeBody(w, r, &m) || !s.nameMatches(w, r, m.Metadata.Name) {
		return
	}
	if err := m.Validate(); err != nil {
		writeError(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, found := s.loadMicroVM(m.Metadata.Name)
	if err := s.checkVersion(found, old.Metadata.ResourceVersion, m.Metadata.ResourceVersion, m.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if found {
		if err := m.ValidateUpdate(&old); err != nil {
			writeError(w, err)
			return
		}
		m.Status = old.Status
		m.Spec.NodeName, m.Spec.BindingEpoch = old.Spec.NodeName, old.Spec.BindingEpoch
		s.storeMicroVM(w, &m, api.EventModified)
		return
	}
	m.Status = api.MicroVMStatus{Phase: api.MicroVMPending}
	s.storeMicroVM(w, &m, api.EventAdded)
}

func (s *memoryServer) bindMicroVM(w http.ResponseWriter, r *http.Request) {
	var b api.Binding
	if !decodeBody(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMicroVM(r.PathValue("name"))
	if err := s.requireVersion(found, m.Metadata.ResourceVersion, b.ResourceVersion, r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	if err := m.Bind(b); err != nil {
		writeError(w, err)
		return
	}
	s.storeMicroVM(w, &m, api.EventModified)
}

func (s *memoryServer) putMicroVMStatus(w http.ResponseWriter, r *http.Request) {
	var next api.MicroVM
	if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMicroVM(next.Metadata.Name)
	if err := s.requireVersion(found, m.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatusUpdate(&m); err != nil {
		writeError(w, err)
		return
	}
	m.Status = next.Status
	s.storeMicroVM(w, &m, api.EventModified)
}

func (s *memoryServer) deleteMicroVM(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	m, found := s.loadMicroVM(name)
	if err := s.requireVersion(found, m.Metadata.ResourceVersion, api.ResourceVersion(r.URL.Query().Get("resourceVersion")), name); err != nil {
		writeError(w, err)
		return
	}
	now := time.Now().UTC()
	m.Metadata.DeletionTimestamp = &now
	if len(m.Metadata.Finalizers) > 0 {
		s.storeMicroVM(w, &m, api.EventModified)
		return
	}
	delete(s.objects, "microvms/"+name)
	s.rev++
	m.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	record(s, api.KindMicroVM, &m, api.EventDeleted)
	writeJSON(w, http.StatusOK, &m)
}

// putValidated answers PUT of an object stored under prefix, with the write
// rule of every PUT: an empty version creates, a version is a compare-and-swap.
// check returns the object's metadata and its validation error.
func putValidated[T any](s *memoryServer, prefix, kind string, check func(*T) (*api.ObjectMeta, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var obj T
		if !decodeBody(w, r, &obj) {
			return
		}
		meta, err := check(&obj)
		if err != nil {
			writeError(w, err)
			return
		}
		if !s.nameMatches(w, r, meta.Name) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		key := prefix + meta.Name
		old, found := load[T](s, key)
		oldMeta, _ := check(&old)
		if err := s.checkVersion(found, oldMeta.ResourceVersion, meta.ResourceVersion, meta.Name); err != nil {
			writeError(w, err)
			return
		}
		t := api.EventAdded
		if found {
			t = api.EventModified
		}
		store(s, w, key, kind, meta, &obj, t)
	}
}

// deleteObject answers DELETE of an object stored under prefix, at the
// version in the query, and logs a Deleted event with its last state.
func deleteObject[T any](s *memoryServer, prefix, kind string, meta func(*T) *api.ObjectMeta) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s.mu.Lock()
		defer s.mu.Unlock()
		obj, found := load[T](s, prefix+name)
		m := meta(&obj)
		if err := s.requireVersion(found, m.ResourceVersion, api.ResourceVersion(r.URL.Query().Get("resourceVersion")), name); err != nil {
			writeError(w, err)
			return
		}
		delete(s.objects, prefix+name)
		s.rev++
		m.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
		record(s, kind, &obj, api.EventDeleted)
		writeJSON(w, http.StatusOK, &obj)
	}
}

// getObject answers GET of one object stored under prefix.
func getObject[T any](s *memoryServer, prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		obj, found := load[T](s, prefix+r.PathValue("name"))
		if !found {
			writeError(w, api.NewAPIError(api.ReasonNotFound, "%q not found", r.PathValue("name")))
			return
		}
		writeJSON(w, http.StatusOK, &obj)
	}
}

// listOrWatch answers GET of a collection: the list build makes of the
// objects under prefix, or with watch=true the events of kind.
func listOrWatch[T any](s *memoryServer, prefix, kind string, build func([]T, api.ResourceVersion) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			s.watch(w, r, kind)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		items := []T{}
		for key := range s.objects {
			if strings.HasPrefix(key, prefix) {
				obj, _ := load[T](s, key)
				items = append(items, obj)
			}
		}
		writeJSON(w, http.StatusOK, build(items, api.ResourceVersion(strconv.FormatInt(s.rev, 10))))
	}
}

func (s *memoryServer) watch(w http.ResponseWriter, r *http.Request, kind string) {
	from, err := strconv.ParseInt(r.URL.Query().Get("resourceVersion"), 10, 64)
	if err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "resourceVersion is not a version"))
		return
	}
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	enc := json.NewEncoder(w)
	for {
		s.mu.Lock()
		if compacted := s.compacted; from < compacted {
			s.mu.Unlock()
			ev, _ := api.NewErrorEvent(api.NewAPIError(api.ReasonExpired, "resourceVersion %d is older than %d", from, compacted))
			_ = enc.Encode(ev)
			flusher.Flush()
			return
		}
		var pending []loggedEvent
		for _, l := range s.log {
			if l.rev > from {
				pending = append(pending, l)
			}
		}
		changed := s.changed
		s.mu.Unlock()
		for _, l := range pending {
			from = l.rev
			if l.kind != kind {
				continue
			}
			if err := enc.Encode(l.event); err != nil {
				return
			}
		}
		flusher.Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

// deleteVolume answers DELETE of a Volume as deleteMicroVM does: a Volume with
// a finalizer gets a deletionTimestamp and stays.
func (s *memoryServer) deleteVolume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	v, found := load[api.Volume](s, "volumes/"+name)
	if err := s.requireVersion(found, v.Metadata.ResourceVersion, api.ResourceVersion(r.URL.Query().Get("resourceVersion")), name); err != nil {
		writeError(w, err)
		return
	}
	now := time.Now().UTC()
	v.Metadata.DeletionTimestamp = &now
	if len(v.Metadata.Finalizers) > 0 {
		store(s, w, "volumes/"+name, api.KindVolume, &v.Metadata, &v, api.EventModified)
		return
	}
	delete(s.objects, "volumes/"+name)
	s.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	record(s, api.KindVolume, &v, api.EventDeleted)
	writeJSON(w, http.StatusOK, &v)
}

func (s *memoryServer) putVolumeStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Volume
	if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "volumes/" + next.Metadata.Name
	raw, found := s.objects[key]
	var v api.Volume
	if found {
		_ = json.Unmarshal(raw, &v)
	}
	if err := s.requireVersion(found, v.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatus(); err != nil {
		writeError(w, err)
		return
	}
	v.Status = next.Status
	s.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects[key] = mustJSON(&v)
	writeJSON(w, http.StatusOK, &v)
}

func (s *memoryServer) putLease(w http.ResponseWriter, r *http.Request) {
	var l api.Lease
	if !decodeBody(w, r, &l) || !s.nameMatches(w, r, l.Metadata.Name) {
		return
	}
	if err := l.Validate(); err != nil {
		writeError(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "leases/" + l.Metadata.Name
	var old api.Lease
	raw, found := s.objects[key]
	if found {
		_ = json.Unmarshal(raw, &old)
	}
	if err := s.checkVersion(found, old.Metadata.ResourceVersion, l.Metadata.ResourceVersion, l.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	t := api.EventAdded
	if found {
		t = api.EventModified
	}
	store(s, w, key, api.KindLease, &l.Metadata, &l, t)
}

func (s *memoryServer) putNode(w http.ResponseWriter, r *http.Request) {
	var n api.Node
	if !decodeBody(w, r, &n) || !s.nameMatches(w, r, n.Metadata.Name) {
		return
	}
	if err := n.Validate(); err != nil {
		writeError(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "nodes/" + n.Metadata.Name
	old, found := load[api.Node](s, key)
	if err := s.checkVersion(found, old.Metadata.ResourceVersion, n.Metadata.ResourceVersion, n.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if found {
		n.Status = old.Status
		store(s, w, key, api.KindNode, &n.Metadata, &n, api.EventModified)
		return
	}
	n.Status = api.NodeStatus{State: api.NodeStateDiscovered}
	store(s, w, key, api.KindNode, &n.Metadata, &n, api.EventAdded)
}

func (s *memoryServer) putNodeStatus(w http.ResponseWriter, r *http.Request) {
	var next api.Node
	if !decodeBody(w, r, &next) || !s.nameMatches(w, r, next.Metadata.Name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "nodes/" + next.Metadata.Name
	n, found := load[api.Node](s, key)
	if err := s.requireVersion(found, n.Metadata.ResourceVersion, next.Metadata.ResourceVersion, next.Metadata.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := next.ValidateStatus(); err != nil {
		writeError(w, err)
		return
	}
	n.Status = next.Status
	store(s, w, key, api.KindNode, &n.Metadata, &n, api.EventModified)
}

func (s *memoryServer) loadMicroVM(name string) (api.MicroVM, bool) {
	var m api.MicroVM
	raw, found := s.objects["microvms/"+name]
	if found {
		_ = json.Unmarshal(raw, &m)
	}
	return m, found
}

func load[T any](s *memoryServer, key string) (T, bool) {
	var obj T
	raw, found := s.objects[key]
	if found {
		_ = json.Unmarshal(raw, &obj)
	}
	return obj, found
}

// storeMicroVM writes m at a new revision, logs the event and answers with m.
func (s *memoryServer) storeMicroVM(w http.ResponseWriter, m *api.MicroVM, t api.EventType) {
	store(s, w, "microvms/"+m.Metadata.Name, api.KindMicroVM, &m.Metadata, m, t)
}

// store writes obj at a new revision, logs the event and answers with obj.
func store[T any](s *memoryServer, w http.ResponseWriter, key, kind string, meta *api.ObjectMeta, obj *T, t api.EventType) {
	s.rev++
	meta.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	s.objects[key] = mustJSON(obj)
	record(s, kind, obj, t)
	writeJSON(w, http.StatusOK, obj)
}

func record[T any](s *memoryServer, kind string, obj *T, t api.EventType) {
	ev, err := api.NewEvent(t, obj)
	if err != nil {
		panic(err)
	}
	s.log = append(s.log, loggedEvent{rev: s.rev, kind: kind, event: ev})
	close(s.changed)
	s.changed = make(chan struct{})
}

// checkVersion is the write rule of PUT: an empty version creates, and
// refuses an object that exists; a version updates, and must be current.
func (s *memoryServer) checkVersion(found bool, current, given api.ResourceVersion, name string) error {
	if given == "" {
		if found {
			return api.NewAPIError(api.ReasonAlreadyExists, "%q already exists", name)
		}
		return nil
	}
	return s.requireVersion(found, current, given, name)
}

// requireVersion is the write rule of every other write: the object exists
// at the given version.
func (s *memoryServer) requireVersion(found bool, current, given api.ResourceVersion, name string) error {
	if !found {
		return api.NewAPIError(api.ReasonNotFound, "%q not found", name)
	}
	if current != given {
		return api.NewAPIError(api.ReasonConflict, "%q is at version %s, not %s", name, current, given)
	}
	return nil
}

func (s *memoryServer) nameMatches(w http.ResponseWriter, r *http.Request, name string) bool {
	if name != r.PathValue("name") {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "metadata.name %q differs from the path", name))
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "decode body: %v", err))
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	apiErr, ok := err.(*api.APIError)
	if !ok {
		apiErr = &api.APIError{Code: http.StatusInternalServerError, Reason: "Internal", Message: err.Error()}
	}
	writeJSON(w, apiErr.Code, apiErr)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode %T: %v", v, err))
	}
	return b
}

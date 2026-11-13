package apiserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/garamsh/gaon/apiserver/internal/authorization"
	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/lib/api"
)

const maxBody = 1 << 20

// root is the group and version root: every path of resource-api.md §Paths is
// relative to it.
const root = "/apis/" + api.GroupVersion

// rbacRoot is the root of the paths of ClusterRoles and ClusterRoleBindings.
const rbacRoot = "/apis/" + api.RBACGroupVersion

// extensionsRoot is the root of the paths of CustomResourceDefinitions.
const extensionsRoot = "/apis/" + api.APIExtensionsGroupVersion

// admissionRoot is the root of the paths of the webhook configurations.
const admissionRoot = "/apis/" + api.AdmissionRegistrationGroupVersion

// coordinationRoot is the root of the paths of the coordination group's Lease.
const coordinationRoot = "/apis/" + api.CoordinationGroupVersion

// customRoot is the collection path of the objects of a registered kind: the
// group, the version and the plural come from the request, and the definition
// that matches them decides whether it is served.
const customRoot = "/apis/{group}/{version}/{plural}"

type traceKey struct{}

// newHandler serves the paths of resource-api.md §Paths under root, rbacRoot,
// extensionsRoot, admissionRoot, coordinationRoot and customRoot over svc, and answers NotFound to any other path. az authorizes
// each request a certificate holder makes. agents calls the Node Agents for a
// MicroVM's log and exec, and is nil where the server has no client
// certificate for them. It is the one place that maps a
// failure to an HTTP status.
func newHandler(svc resourceapi.Service, az authorization.Authorizer, tokens tokenSet, accounts *accountTokens, agents *agentClient, log *slog.Logger) http.Handler {
	h := &handler{log: log, az: az}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, api.NewAPIError(api.ReasonNotFound, "no such path"))
	})
	mux.HandleFunc("PUT "+root+"/microvms/{name}", h.guard(api.Group, "microvms", putVerb, h.putMicroVM(svc)))
	mux.HandleFunc("POST "+root+"/microvms/{name}/binding", h.guard(api.Group, "microvms/binding", fixed(api.VerbCreate), h.bind(svc)))
	mux.HandleFunc("PUT "+root+"/microvms/{name}/status", h.guard(api.Group, "microvms/status", fixed(api.VerbUpdate), put(h, svc.PutMicroVMStatus)))
	mux.HandleFunc("DELETE "+root+"/microvms/{name}", h.guard(api.Group, "microvms", fixed(api.VerbDelete), deleteObject(h, svc.DeleteMicroVM)))
	mux.HandleFunc("GET "+root+"/microvms/{name}", h.guard(api.Group, "microvms", fixed(api.VerbGet), get(h, svc.GetMicroVM)))
	mux.HandleFunc("GET "+root+"/microvms/{name}/log", h.guard(api.Group, "microvms/log", fixed(api.VerbGet), h.microVMLog(svc, agents)))
	mux.HandleFunc("POST "+root+"/microvms/{name}/exec", h.guard(api.Group, "microvms/exec", fixed(api.VerbCreate), h.microVMExec(svc, agents)))
	mux.HandleFunc("GET "+root+"/microvms", h.guard(api.Group, "microvms", readVerb, listOrWatch(h, svc.ListMicroVMs, svc.WatchMicroVMs)))
	mux.HandleFunc("PUT "+root+"/volumes/{name}", h.guard(api.Group, "volumes", putVerb, h.putVolume(svc)))
	mux.HandleFunc("DELETE "+root+"/volumes/{name}", h.guard(api.Group, "volumes", fixed(api.VerbDelete), deleteObject(h, svc.DeleteVolume)))
	mux.HandleFunc("GET "+root+"/volumes/{name}", h.guard(api.Group, "volumes", fixed(api.VerbGet), get(h, svc.GetVolume)))
	mux.HandleFunc("GET "+root+"/volumes", h.guard(api.Group, "volumes", readVerb, listOrWatch(h, svc.ListVolumes, svc.WatchVolumes)))
	mux.HandleFunc("PUT "+root+"/volumes/{name}/status", h.guard(api.Group, "volumes/status", fixed(api.VerbUpdate), put(h, svc.PutVolumeStatus)))
	mux.HandleFunc("PUT "+root+"/volumesnapshots/{name}", h.guard(api.Group, "volumesnapshots", putVerb, h.putVolumeSnapshot(svc)))
	mux.HandleFunc("DELETE "+root+"/volumesnapshots/{name}", h.guard(api.Group, "volumesnapshots", fixed(api.VerbDelete), deleteObject(h, svc.DeleteVolumeSnapshot)))
	mux.HandleFunc("GET "+root+"/volumesnapshots/{name}", h.guard(api.Group, "volumesnapshots", fixed(api.VerbGet), get(h, svc.GetVolumeSnapshot)))
	mux.HandleFunc("GET "+root+"/volumesnapshots", h.guard(api.Group, "volumesnapshots", readVerb, listOrWatch(h, svc.ListVolumeSnapshots, svc.WatchVolumeSnapshots)))
	mux.HandleFunc("PUT "+root+"/volumesnapshots/{name}/status", h.guard(api.Group, "volumesnapshots/status", fixed(api.VerbUpdate), put(h, svc.PutVolumeSnapshotStatus)))
	mux.HandleFunc("PUT "+root+"/storageclasses/{name}", h.guard(api.Group, "storageclasses", putVerb, put(h, svc.PutStorageClass)))
	mux.HandleFunc("DELETE "+root+"/storageclasses/{name}", h.guard(api.Group, "storageclasses", fixed(api.VerbDelete), deleteObject(h, svc.DeleteStorageClass)))
	mux.HandleFunc("GET "+root+"/storageclasses/{name}", h.guard(api.Group, "storageclasses", fixed(api.VerbGet), get(h, svc.GetStorageClass)))
	mux.HandleFunc("GET "+root+"/storageclasses", h.guard(api.Group, "storageclasses", readVerb, listOrWatch(h, svc.ListStorageClasses, svc.WatchStorageClasses)))
	mux.HandleFunc("PUT "+root+"/nodes/{name}", h.guard(api.Group, "nodes", putVerb, put(h, svc.PutNode)))
	mux.HandleFunc("PUT "+root+"/nodes/{name}/status", h.guard(api.Group, "nodes/status", fixed(api.VerbUpdate), put(h, svc.PutNodeStatus)))
	mux.HandleFunc("GET "+root+"/nodes/{name}", h.guard(api.Group, "nodes", fixed(api.VerbGet), get(h, svc.GetNode)))
	mux.HandleFunc("GET "+root+"/nodes", h.guard(api.Group, "nodes", readVerb, listOrWatch(h, svc.ListNodes, svc.WatchNodes)))
	mux.HandleFunc("PUT "+root+"/leases/{name}", h.guard(api.Group, "leases", putVerb, put(h, svc.PutLease)))
	mux.HandleFunc("GET "+root+"/leases/{name}", h.guard(api.Group, "leases", fixed(api.VerbGet), get(h, svc.GetLease)))
	mux.HandleFunc("GET "+root+"/leases", h.guard(api.Group, "leases", readVerb, listOrWatch(h, svc.ListLeases, svc.WatchLeases)))
	mux.HandleFunc("PUT "+root+"/events/{name}", h.guard(api.Group, "events", putVerb, put(h, svc.PutClusterEvent)))
	mux.HandleFunc("DELETE "+root+"/events/{name}", h.guard(api.Group, "events", fixed(api.VerbDelete), deleteObject(h, svc.DeleteClusterEvent)))
	mux.HandleFunc("GET "+root+"/events/{name}", h.guard(api.Group, "events", fixed(api.VerbGet), get(h, svc.GetClusterEvent)))
	mux.HandleFunc("GET "+root+"/events", h.guard(api.Group, "events", readVerb, listOrWatch(h, svc.ListClusterEvents, svc.WatchClusterEvents)))
	mux.HandleFunc("PUT "+root+"/secrets/{name}", h.guard(api.Group, "secrets", putVerb, put(h, svc.PutSecret)))
	mux.HandleFunc("DELETE "+root+"/secrets/{name}", h.guard(api.Group, "secrets", fixed(api.VerbDelete), deleteObject(h, svc.DeleteSecret)))
	mux.HandleFunc("GET "+root+"/secrets/{name}", h.guard(api.Group, "secrets", fixed(api.VerbGet), get(h, svc.GetSecret)))
	mux.HandleFunc("GET "+root+"/secrets", h.guard(api.Group, "secrets", readVerb, listOrWatch(h, svc.ListSecrets, svc.WatchSecrets)))
	mux.HandleFunc("PUT "+root+"/services/{name}", h.guard(api.Group, "services", putVerb, put(h, svc.PutService)))
	mux.HandleFunc("DELETE "+root+"/services/{name}", h.guard(api.Group, "services", fixed(api.VerbDelete), deleteObject(h, svc.DeleteService)))
	mux.HandleFunc("GET "+root+"/services/{name}", h.guard(api.Group, "services", fixed(api.VerbGet), get(h, svc.GetService)))
	mux.HandleFunc("GET "+root+"/services", h.guard(api.Group, "services", readVerb, listOrWatch(h, svc.ListServices, svc.WatchServices)))
	mux.HandleFunc("PUT "+root+"/endpoints/{name}", h.guard(api.Group, "endpoints", putVerb, put(h, svc.PutEndpoints)))
	mux.HandleFunc("DELETE "+root+"/endpoints/{name}", h.guard(api.Group, "endpoints", fixed(api.VerbDelete), deleteObject(h, svc.DeleteEndpoints)))
	mux.HandleFunc("GET "+root+"/endpoints/{name}", h.guard(api.Group, "endpoints", fixed(api.VerbGet), get(h, svc.GetEndpoints)))
	mux.HandleFunc("GET "+root+"/endpoints", h.guard(api.Group, "endpoints", readVerb, listOrWatch(h, svc.ListEndpoints, svc.WatchEndpoints)))
	mux.HandleFunc("PUT "+root+"/networkpolicies/{name}", h.guard(api.Group, "networkpolicies", putVerb, put(h, svc.PutNetworkPolicy)))
	mux.HandleFunc("DELETE "+root+"/networkpolicies/{name}", h.guard(api.Group, "networkpolicies", fixed(api.VerbDelete), deleteObject(h, svc.DeleteNetworkPolicy)))
	mux.HandleFunc("GET "+root+"/networkpolicies/{name}", h.guard(api.Group, "networkpolicies", fixed(api.VerbGet), get(h, svc.GetNetworkPolicy)))
	mux.HandleFunc("GET "+root+"/networkpolicies", h.guard(api.Group, "networkpolicies", readVerb, listOrWatch(h, svc.ListNetworkPolicies, svc.WatchNetworkPolicies)))
	mux.HandleFunc("PUT "+root+"/configmaps/{name}", h.guard(api.Group, "configmaps", putVerb, put(h, svc.PutConfigMap)))
	mux.HandleFunc("DELETE "+root+"/configmaps/{name}", h.guard(api.Group, "configmaps", fixed(api.VerbDelete), deleteObject(h, svc.DeleteConfigMap)))
	mux.HandleFunc("GET "+root+"/configmaps/{name}", h.guard(api.Group, "configmaps", fixed(api.VerbGet), get(h, svc.GetConfigMap)))
	mux.HandleFunc("GET "+root+"/configmaps", h.guard(api.Group, "configmaps", readVerb, listOrWatch(h, svc.ListConfigMaps, svc.WatchConfigMaps)))
	mux.HandleFunc("PUT "+root+"/serviceaccounts/{name}", h.guard(api.Group, "serviceaccounts", putVerb, put(h, svc.PutServiceAccount)))
	mux.HandleFunc("DELETE "+root+"/serviceaccounts/{name}", h.guard(api.Group, "serviceaccounts", fixed(api.VerbDelete), deleteObject(h, svc.DeleteServiceAccount)))
	mux.HandleFunc("GET "+root+"/serviceaccounts/{name}", h.guard(api.Group, "serviceaccounts", fixed(api.VerbGet), get(h, svc.GetServiceAccount)))
	mux.HandleFunc("GET "+root+"/serviceaccounts", h.guard(api.Group, "serviceaccounts", readVerb, listOrWatch(h, svc.ListServiceAccounts, svc.WatchServiceAccounts)))
	mux.HandleFunc("POST "+root+"/serviceaccounts/{name}/token", h.guard(api.Group, "serviceaccounts/token", fixed(api.VerbCreate), put(h, svc.CreateServiceAccountToken)))

	mux.HandleFunc("PUT "+coordinationRoot+"/leases/{name}", h.guard(api.CoordinationGroup, "leases", putVerb, put(h, svc.PutCoordinationLease)))
	mux.HandleFunc("DELETE "+coordinationRoot+"/leases/{name}", h.guard(api.CoordinationGroup, "leases", fixed(api.VerbDelete), deleteObject(h, svc.DeleteCoordinationLease)))
	mux.HandleFunc("GET "+coordinationRoot+"/leases/{name}", h.guard(api.CoordinationGroup, "leases", fixed(api.VerbGet), get(h, svc.GetCoordinationLease)))
	mux.HandleFunc("GET "+coordinationRoot+"/leases", h.guard(api.CoordinationGroup, "leases", readVerb, listOrWatch(h, svc.ListCoordinationLeases, svc.WatchCoordinationLeases)))
	mux.HandleFunc(createCSRRoute, h.guard(api.Group, "certificatesigningrequests", fixed(api.VerbCreate), h.createCertificateSigningRequest(svc)))
	mux.HandleFunc("PUT "+root+"/certificatesigningrequests/{name}/status", h.guard(api.Group, "certificatesigningrequests/status", fixed(api.VerbUpdate), put(h, svc.PutCertificateSigningRequestStatus)))
	mux.HandleFunc(getCSRRoute, h.guard(api.Group, "certificatesigningrequests", fixed(api.VerbGet), h.getCertificateSigningRequest(svc)))
	mux.HandleFunc("GET "+root+"/certificatesigningrequests", h.guard(api.Group, "certificatesigningrequests", readVerb, listOrWatch(h, svc.ListCertificateSigningRequests, svc.WatchCertificateSigningRequests)))

	mux.HandleFunc("PUT "+rbacRoot+"/clusterroles/{name}", h.guard(api.RBACGroup, "clusterroles", putVerb, h.putClusterRole(svc)))
	mux.HandleFunc("DELETE "+rbacRoot+"/clusterroles/{name}", h.guard(api.RBACGroup, "clusterroles", fixed(api.VerbDelete), deleteObject(h, svc.DeleteClusterRole)))
	mux.HandleFunc("GET "+rbacRoot+"/clusterroles/{name}", h.guard(api.RBACGroup, "clusterroles", fixed(api.VerbGet), get(h, svc.GetClusterRole)))
	mux.HandleFunc("GET "+rbacRoot+"/clusterroles", h.guard(api.RBACGroup, "clusterroles", readVerb, listOrWatch(h, svc.ListClusterRoles, svc.WatchClusterRoles)))
	mux.HandleFunc("PUT "+rbacRoot+"/clusterrolebindings/{name}", h.guard(api.RBACGroup, "clusterrolebindings", putVerb, h.putClusterRoleBinding(svc)))
	mux.HandleFunc("DELETE "+rbacRoot+"/clusterrolebindings/{name}", h.guard(api.RBACGroup, "clusterrolebindings", fixed(api.VerbDelete), deleteObject(h, svc.DeleteClusterRoleBinding)))
	mux.HandleFunc("GET "+rbacRoot+"/clusterrolebindings/{name}", h.guard(api.RBACGroup, "clusterrolebindings", fixed(api.VerbGet), get(h, svc.GetClusterRoleBinding)))
	mux.HandleFunc("GET "+rbacRoot+"/clusterrolebindings", h.guard(api.RBACGroup, "clusterrolebindings", readVerb, listOrWatch(h, svc.ListClusterRoleBindings, svc.WatchClusterRoleBindings)))

	mux.HandleFunc("PUT "+extensionsRoot+"/customresourcedefinitions/{name}", h.guard(api.APIExtensionsGroup, "customresourcedefinitions", putVerb, put(h, svc.PutCustomResourceDefinition)))
	mux.HandleFunc("DELETE "+extensionsRoot+"/customresourcedefinitions/{name}", h.guard(api.APIExtensionsGroup, "customresourcedefinitions", fixed(api.VerbDelete), deleteObject(h, svc.DeleteCustomResourceDefinition)))
	mux.HandleFunc("GET "+extensionsRoot+"/customresourcedefinitions/{name}", h.guard(api.APIExtensionsGroup, "customresourcedefinitions", fixed(api.VerbGet), get(h, svc.GetCustomResourceDefinition)))
	mux.HandleFunc("GET "+extensionsRoot+"/customresourcedefinitions", h.guard(api.APIExtensionsGroup, "customresourcedefinitions", readVerb, listOrWatch(h, svc.ListCustomResourceDefinitions, svc.WatchCustomResourceDefinitions)))

	custom := func(sub string) func(*http.Request) (string, string) {
		return func(r *http.Request) (string, string) { return r.PathValue("group"), r.PathValue("plural") + sub }
	}
	mux.HandleFunc("PUT "+admissionRoot+"/mutatingwebhookconfigurations/{name}", h.guard(api.AdmissionRegistrationGroup, "mutatingwebhookconfigurations", putVerb, put(h, svc.PutMutatingWebhookConfiguration)))
	mux.HandleFunc("DELETE "+admissionRoot+"/mutatingwebhookconfigurations/{name}", h.guard(api.AdmissionRegistrationGroup, "mutatingwebhookconfigurations", fixed(api.VerbDelete), deleteObject(h, svc.DeleteMutatingWebhookConfiguration)))
	mux.HandleFunc("GET "+admissionRoot+"/mutatingwebhookconfigurations/{name}", h.guard(api.AdmissionRegistrationGroup, "mutatingwebhookconfigurations", fixed(api.VerbGet), get(h, svc.GetMutatingWebhookConfiguration)))
	mux.HandleFunc("GET "+admissionRoot+"/mutatingwebhookconfigurations", h.guard(api.AdmissionRegistrationGroup, "mutatingwebhookconfigurations", readVerb, listOrWatch(h, svc.ListMutatingWebhookConfigurations, svc.WatchMutatingWebhookConfigurations)))
	mux.HandleFunc("PUT "+admissionRoot+"/validatingwebhookconfigurations/{name}", h.guard(api.AdmissionRegistrationGroup, "validatingwebhookconfigurations", putVerb, put(h, svc.PutValidatingWebhookConfiguration)))
	mux.HandleFunc("DELETE "+admissionRoot+"/validatingwebhookconfigurations/{name}", h.guard(api.AdmissionRegistrationGroup, "validatingwebhookconfigurations", fixed(api.VerbDelete), deleteObject(h, svc.DeleteValidatingWebhookConfiguration)))
	mux.HandleFunc("GET "+admissionRoot+"/validatingwebhookconfigurations/{name}", h.guard(api.AdmissionRegistrationGroup, "validatingwebhookconfigurations", fixed(api.VerbGet), get(h, svc.GetValidatingWebhookConfiguration)))
	mux.HandleFunc("GET "+admissionRoot+"/validatingwebhookconfigurations", h.guard(api.AdmissionRegistrationGroup, "validatingwebhookconfigurations", readVerb, listOrWatch(h, svc.ListValidatingWebhookConfigurations, svc.WatchValidatingWebhookConfigurations)))

	mux.HandleFunc("PUT "+customRoot+"/{name}", h.guardBy(custom(""), putVerb, h.putCustomObject(svc.PutCustomObject)))
	mux.HandleFunc("PUT "+customRoot+"/{name}/status", h.guardBy(custom("/status"), fixed(api.VerbUpdate), h.putCustomObject(svc.PutCustomObjectStatus)))
	mux.HandleFunc("DELETE "+customRoot+"/{name}", h.guardBy(custom(""), fixed(api.VerbDelete), h.deleteCustomObject(svc)))
	mux.HandleFunc("GET "+customRoot+"/{name}", h.guardBy(custom(""), fixed(api.VerbGet), h.getCustomObject(svc)))
	mux.HandleFunc("GET "+customRoot, h.guardBy(custom(""), readVerb, h.listOrWatchCustomObjects(svc)))
	if accounts != nil {
		serveDiscovery(mux, accounts.verifier)
	}
	return h.recoverPanics(h.authenticate(mux, tokens, accounts))
}

// The routes a bearer-token holder may reach.
const (
	createCSRRoute = "PUT " + root + "/certificatesigningrequests/{name}"
	getCSRRoute    = "GET " + root + "/certificatesigningrequests/{name}"
)

type handler struct {
	log *slog.Logger
	az  authorization.Authorizer
}

// verbOf is the verb of a request, which the method says except for a PUT and a
// GET of a collection.
type verbOf func(*http.Request) (api.Verb, error)

func fixed(v api.Verb) verbOf {
	return func(*http.Request) (api.Verb, error) { return v, nil }
}

// readVerb is watch for a collection read with watch=true, and list for any
// other.
func readVerb(r *http.Request) (api.Verb, error) {
	if r.URL.Query().Get("watch") == "true" {
		return api.VerbWatch, nil
	}
	return api.VerbList, nil
}

// bodyMetadata is what authorization reads of the metadata in a request body.
type bodyMetadata struct {
	ResourceVersion api.ResourceVersion `json:"resourceVersion"`
	Labels          map[string]string   `json:"labels"`
}

// peekMetadata reads the metadata of the body without consuming it. A body
// that does not parse has none: the handler refuses it.
func peekMetadata(r *http.Request) (bodyMetadata, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return bodyMetadata{}, api.NewAPIError(api.ReasonInvalid, "read body: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var peek struct {
		Metadata bodyMetadata `json:"metadata"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return bodyMetadata{}, nil
	}
	return peek.Metadata, nil
}

// peekBound reads the MicroVM a token request binds from the body without
// consuming it. A body that does not parse binds none: the handler refuses it.
func peekBound(r *http.Request) (api.BoundObject, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return api.BoundObject{}, api.NewAPIError(api.ReasonInvalid, "read body: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var peek struct {
		Spec struct {
			BoundMicroVM api.BoundObject `json:"boundMicroVM"`
		} `json:"spec"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return api.BoundObject{}, nil
	}
	return peek.Spec.BoundMicroVM, nil
}

// putVerb is update for a body that carries a resourceVersion and create for
// one that does not. It leaves the body to be read again.
func putVerb(r *http.Request) (api.Verb, error) {
	meta, err := peekMetadata(r)
	if err != nil {
		return "", err
	}
	if meta.ResourceVersion != "" {
		return api.VerbUpdate, nil
	}
	return api.VerbCreate, nil
}

// selectorOf is the label selector of a list or a watch, and nil where the
// request has none. A selector that does not parse is an Invalid failure.
func selectorOf(r *http.Request) (api.Selector, error) {
	sel, err := api.ParseSelector(r.URL.Query().Get(api.LabelSelectorParam))
	if err != nil {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s: %v", api.LabelSelectorParam, err)
	}
	return sel, nil
}

// caller is the identity as the authorizers see it.
func (id identity) caller() authorization.Caller {
	return authorization.Caller{User: id.name, Groups: id.groups}
}

// guard runs next for a caller the authorizer allows to make the verb on the
// resource, which is "microvms" or, for a subresource, "microvms/status". A
// bearer-token holder has reached only the routes of ADR 0007 and is not
// authorized here.
func (h *handler) guard(group, resource string, verb verbOf, next http.HandlerFunc) http.HandlerFunc {
	return h.guardBy(func(*http.Request) (string, string) { return group, resource }, verb, next)
}

// guardBy is guard for a route whose group and resource the request names, as
// those of a custom resource are.
func (h *handler) guardBy(resourceOf func(*http.Request) (group, resource string), verb verbOf, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := callerOf(r)
		if id.token {
			next(w, r)
			return
		}
		v, err := verb(r)
		if err == nil {
			group, resource := resourceOf(r)
			name, sub, _ := strings.Cut(resource, "/")
			req := authorization.Request{Group: group, Resource: name, Subresource: sub, Verb: v, Name: r.PathValue("name")}
			err = h.scope(r, &req)
			if err == nil {
				err = h.az.Authorize(r.Context(), id.caller(), req)
			}
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r)
	}
}

// scope sets on req what a rule with a label selector judges that is not
// stored: the labels in the body of a create or an update of an object, and
// the selector of a list or a watch. A selector that does not parse is left
// empty, the widest, so that no rule with a selector allows the request, and
// the handler answers it as Invalid.
func (h *handler) scope(r *http.Request, req *authorization.Request) error {
	switch {
	case req.Verb == api.VerbList || req.Verb == api.VerbWatch:
		req.Selector, _ = selectorOf(r)
	case req.Resource == "serviceaccounts" && req.Subresource == "token":
		bound, err := peekBound(r)
		req.BoundMicroVM = bound
		return err
	case (req.Verb == api.VerbCreate || req.Verb == api.VerbUpdate) && req.Subresource == "":
		meta, err := peekMetadata(r)
		req.Labels = meta.Labels
		return err
	}
	return nil
}

// recoverPanics answers 500 where a handler panics, which net/http would
// answer by closing the connection, and gives each request a trace id.
func (h *handler) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), traceKey{}, newTraceID()))
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			h.log.Error("handler panicked", "trace_id", traceID(r), "method", r.Method, "path", r.URL.Path, "panic", rec)
			writeJSON(w, http.StatusInternalServerError, internal("internal error"))
		}()
		next.ServeHTTP(w, r)
	})
}

func put[T any](h *handler, call func(context.Context, string, *T) (*T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in T
		if !h.decode(w, r, &in) {
			return
		}
		out, err := call(r.Context(), r.PathValue("name"), &in)
		h.respond(w, r, out, err)
	}
}

func get[T any](h *handler, call func(context.Context, string) (*T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := call(r.Context(), r.PathValue("name"))
		h.respond(w, r, out, err)
	}
}

func (h *handler) bind(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b api.Binding
		if !h.decode(w, r, &b) {
			return
		}
		out, err := svc.BindMicroVM(r.Context(), r.PathValue("name"), b)
		h.respond(w, r, out, err)
	}
}

// createCertificateSigningRequest creates a request whose spec.username is the
// caller's identity.
func (h *handler) createCertificateSigningRequest(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.CertificateSigningRequest
		if !h.decode(w, r, &in) {
			return
		}
		out, err := svc.PutCertificateSigningRequest(r.Context(), r.PathValue("name"), callerOf(r).name, &in)
		h.respond(w, r, out, err)
	}
}

// getCertificateSigningRequest answers a token holder only for a request that
// holder created.
func (h *handler) getCertificateSigningRequest(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.GetCertificateSigningRequest(r.Context(), r.PathValue("name"))
		if id := callerOf(r); err == nil && id.token && out.Spec.Username != id.name {
			out, err = nil, api.NewAPIError(api.ReasonForbidden, "the certificatesigningrequest was created by another identity")
		}
		h.respond(w, r, out, err)
	}
}

// deleteObject removes the object the resourceVersion of the query names.
func deleteObject[T any](h *handler, call func(context.Context, string, api.ResourceVersion) (*T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rv := api.ResourceVersion(r.URL.Query().Get("resourceVersion"))
		out, err := call(r.Context(), r.PathValue("name"), rv)
		h.respond(w, r, out, err)
	}
}

// putMicroVM writes a MicroVM, and an update of one only as far as the
// caller's grant reaches: a Node's grant is its finalizer.
func (h *handler) putMicroVM(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.MicroVM
		if !h.decode(w, r, &in) {
			return
		}
		name := r.PathValue("name")
		if in.Metadata.ResourceVersion != "" {
			if err := h.az.AuthorizeMicroVMUpdate(r.Context(), callerOf(r).caller(), name, &in); err != nil {
				h.fail(w, r, err)
				return
			}
		}
		out, err := svc.PutMicroVM(r.Context(), name, &in)
		h.respond(w, r, out, err)
	}
}

// putVolume writes a Volume, and an update of one only as far as the caller's
// grant reaches: a Node's grant is its finalizer.
func (h *handler) putVolume(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.Volume
		if !h.decode(w, r, &in) {
			return
		}
		name := r.PathValue("name")
		if in.Metadata.ResourceVersion != "" {
			if err := h.az.AuthorizeVolumeUpdate(r.Context(), callerOf(r).caller(), name, &in); err != nil {
				h.fail(w, r, err)
				return
			}
		}
		out, err := svc.PutVolume(r.Context(), name, &in)
		h.respond(w, r, out, err)
	}
}

// putVolumeSnapshot writes a VolumeSnapshot, and an update of one only as far
// as the caller's grant reaches: a Node's grant is its finalizer.
func (h *handler) putVolumeSnapshot(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.VolumeSnapshot
		if !h.decode(w, r, &in) {
			return
		}
		name := r.PathValue("name")
		if in.Metadata.ResourceVersion != "" {
			if err := h.az.AuthorizeVolumeSnapshotUpdate(r.Context(), callerOf(r).caller(), name, &in); err != nil {
				h.fail(w, r, err)
				return
			}
		}
		out, err := svc.PutVolumeSnapshot(r.Context(), name, &in)
		h.respond(w, r, out, err)
	}
}

// putClusterRole writes a ClusterRole only for a caller that holds every
// permission its rules grant.
func (h *handler) putClusterRole(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.ClusterRole
		if !h.decode(w, r, &in) {
			return
		}
		if err := in.Validate(); err != nil {
			h.fail(w, r, err)
			return
		}
		if err := h.az.AuthorizeRole(r.Context(), callerOf(r).caller(), in.Rules); err != nil {
			h.fail(w, r, err)
			return
		}
		out, err := svc.PutClusterRole(r.Context(), r.PathValue("name"), &in)
		h.respond(w, r, out, err)
	}
}

// putClusterRoleBinding writes a ClusterRoleBinding only for a caller that
// holds every permission of the role it references.
func (h *handler) putClusterRoleBinding(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.ClusterRoleBinding
		if !h.decode(w, r, &in) {
			return
		}
		if err := in.Validate(); err != nil {
			h.fail(w, r, err)
			return
		}
		if err := h.az.AuthorizeBinding(r.Context(), callerOf(r).caller(), in.RoleRef); err != nil {
			h.fail(w, r, err)
			return
		}
		out, err := svc.PutClusterRoleBinding(r.Context(), r.PathValue("name"), &in)
		h.respond(w, r, out, err)
	}
}

// listOrWatch answers GET of a collection: the list, or with watch=true the
// events after the resourceVersion, of the objects the labelSelector selects.
func listOrWatch[T any](h *handler, list func(context.Context, api.Selector) (*T, error), watch func(context.Context, api.ResourceVersion, api.Selector) (<-chan api.WatchEvent, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sel, err := selectorOf(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if r.URL.Query().Get("watch") != "true" {
			out, err := list(r.Context(), sel)
			h.respond(w, r, out, err)
			return
		}
		events, err := watch(r.Context(), api.ResourceVersion(r.URL.Query().Get("resourceVersion")), sel)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush(flusher)
		enc := json.NewEncoder(w)
		for ev := range events {
			if err := enc.Encode(ev); err != nil {
				return
			}
			flush(flusher)
		}
	}
}

func flush(f http.Flusher) {
	if f != nil {
		f.Flush()
	}
}

// decode reads the JSON body into into and refuses an unknown field, a
// trailing value and a body over maxBody. It answers the refusal itself.
func (h *handler) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(into)
	if err == nil && dec.More() {
		err = errors.New("the body holds more than one value")
	}
	if err != nil {
		h.fail(w, r, api.NewAPIError(api.ReasonInvalid, "decode body: %v", err))
		return false
	}
	return true
}

func (h *handler) respond(w http.ResponseWriter, r *http.Request, out any, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// fail answers err with its error object. An error that is not an APIError
// is the server's: it is logged once, here, and answered as 500.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		h.log.Error("request failed", "trace_id", traceID(r), "method", r.Method, "path", r.URL.Path, "error", err)
		apiErr = internal("internal error")
	}
	writeJSON(w, apiErr.Code, apiErr)
}

func internal(msg string) *api.APIError {
	return &api.APIError{Code: http.StatusInternalServerError, Reason: resourceapi.ReasonInternal, Message: msg}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func traceID(r *http.Request) string {
	id, _ := r.Context().Value(traceKey{}).(string)
	return id
}

func newTraceID() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

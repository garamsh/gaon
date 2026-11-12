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

// customRoot is the collection path of the objects of a registered kind: the
// group, the version and the plural come from the request, and the definition
// that matches them decides whether it is served.
const customRoot = "/apis/{group}/{version}/{plural}"

type traceKey struct{}

// newHandler serves the paths of resource-api.md §Paths under root, rbacRoot,
// extensionsRoot and customRoot over svc, and answers NotFound to any other path. az authorizes
// each request a certificate holder makes. It is the one place that maps a
// failure to an HTTP status.
func newHandler(svc resourceapi.Service, az authorization.Authorizer, tokens tokenSet, log *slog.Logger) http.Handler {
	h := &handler{log: log, az: az}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, api.NewAPIError(api.ReasonNotFound, "no such path"))
	})
	mux.HandleFunc("PUT "+root+"/machines/{name}", h.guard(api.Group, "machines", putVerb, h.putMachine(svc)))
	mux.HandleFunc("POST "+root+"/machines/{name}/binding", h.guard(api.Group, "machines/binding", fixed(api.VerbCreate), h.bind(svc)))
	mux.HandleFunc("PUT "+root+"/machines/{name}/status", h.guard(api.Group, "machines/status", fixed(api.VerbUpdate), put(h, svc.PutMachineStatus)))
	mux.HandleFunc("DELETE "+root+"/machines/{name}", h.guard(api.Group, "machines", fixed(api.VerbDelete), deleteObject(h, svc.DeleteMachine)))
	mux.HandleFunc("GET "+root+"/machines/{name}", h.guard(api.Group, "machines", fixed(api.VerbGet), get(h, svc.GetMachine)))
	mux.HandleFunc("GET "+root+"/machines", h.guard(api.Group, "machines", readVerb, listOrWatch(h, svc.ListMachines, svc.WatchMachines)))
	mux.HandleFunc("PUT "+root+"/volumes/{name}/status", h.guard(api.Group, "volumes/status", fixed(api.VerbUpdate), put(h, svc.PutVolumeStatus)))
	mux.HandleFunc("PUT "+root+"/nodes/{name}", h.guard(api.Group, "nodes", putVerb, put(h, svc.PutNode)))
	mux.HandleFunc("PUT "+root+"/nodes/{name}/status", h.guard(api.Group, "nodes/status", fixed(api.VerbUpdate), put(h, svc.PutNodeStatus)))
	mux.HandleFunc("GET "+root+"/nodes/{name}", h.guard(api.Group, "nodes", fixed(api.VerbGet), get(h, svc.GetNode)))
	mux.HandleFunc("GET "+root+"/nodes", h.guard(api.Group, "nodes", readVerb, listOrWatch(h, svc.ListNodes, svc.WatchNodes)))
	mux.HandleFunc("PUT "+root+"/leases/{name}", h.guard(api.Group, "leases", putVerb, put(h, svc.PutLease)))
	mux.HandleFunc("GET "+root+"/leases/{name}", h.guard(api.Group, "leases", fixed(api.VerbGet), get(h, svc.GetLease)))
	mux.HandleFunc("GET "+root+"/leases", h.guard(api.Group, "leases", readVerb, listOrWatch(h, svc.ListLeases, svc.WatchLeases)))
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
	return h.recoverPanics(h.authenticate(mux, tokens))
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

// putVerb is update for a body that carries a resourceVersion and create for
// one that does not. It leaves the body to be read again.
func putVerb(r *http.Request) (api.Verb, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return "", api.NewAPIError(api.ReasonInvalid, "read body: %v", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var peek struct {
		Metadata struct {
			ResourceVersion api.ResourceVersion `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &peek) == nil && peek.Metadata.ResourceVersion != "" {
		return api.VerbUpdate, nil
	}
	return api.VerbCreate, nil
}

// caller is the identity as the authorizers see it.
func (id identity) caller() authorization.Caller {
	return authorization.Caller{User: id.name, Groups: id.groups}
}

// guard runs next for a caller the authorizer allows to make the verb on the
// resource, which is "machines" or, for a subresource, "machines/status". A
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
			err = h.az.Authorize(r.Context(), id.caller(), authorization.Request{Group: group, Resource: name, Subresource: sub, Verb: v, Name: r.PathValue("name")})
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r)
	}
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
		out, err := svc.BindMachine(r.Context(), r.PathValue("name"), b)
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

// putMachine writes a Machine, and an update of one only as far as the
// caller's grant reaches: a Node's grant is its finalizer.
func (h *handler) putMachine(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.Machine
		if !h.decode(w, r, &in) {
			return
		}
		name := r.PathValue("name")
		if in.Metadata.ResourceVersion != "" {
			if err := h.az.AuthorizeMachineUpdate(r.Context(), callerOf(r).caller(), name, &in); err != nil {
				h.fail(w, r, err)
				return
			}
		}
		out, err := svc.PutMachine(r.Context(), name, &in)
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
// events after the resourceVersion.
func listOrWatch[T any](h *handler, list func(context.Context) (*T, error), watch func(context.Context, api.ResourceVersion) (<-chan api.WatchEvent, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			out, err := list(r.Context())
			h.respond(w, r, out, err)
			return
		}
		events, err := watch(r.Context(), api.ResourceVersion(r.URL.Query().Get("resourceVersion")))
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

package apiserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/lib/api"
)

const maxBody = 1 << 20

// root is the group and version root: every path of resource-api.md §Paths is
// relative to it.
const root = "/apis/" + api.GroupVersion

type traceKey struct{}

// newHandler serves the paths of resource-api.md §Paths under root over svc,
// and answers NotFound to any other path. It is the one place that maps a
// failure to an HTTP status.
func newHandler(svc resourceapi.Service, tokens tokenSet, log *slog.Logger) http.Handler {
	h := &handler{log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, api.NewAPIError(api.ReasonNotFound, "no such path"))
	})
	mux.HandleFunc("PUT "+root+"/machines/{name}", put(h, svc.PutMachine))
	mux.HandleFunc("POST "+root+"/machines/{name}/binding", h.bind(svc))
	mux.HandleFunc("PUT "+root+"/machines/{name}/status", put(h, svc.PutMachineStatus))
	mux.HandleFunc("DELETE "+root+"/machines/{name}", h.deleteMachine(svc))
	mux.HandleFunc("GET "+root+"/machines/{name}", get(h, svc.GetMachine))
	mux.HandleFunc("GET "+root+"/machines", listOrWatch(h, svc.ListMachines, svc.WatchMachines))
	mux.HandleFunc("PUT "+root+"/volumes/{name}/status", put(h, svc.PutVolumeStatus))
	mux.HandleFunc("PUT "+root+"/nodes/{name}", put(h, svc.PutNode))
	mux.HandleFunc("PUT "+root+"/nodes/{name}/status", put(h, svc.PutNodeStatus))
	mux.HandleFunc("GET "+root+"/nodes/{name}", get(h, svc.GetNode))
	mux.HandleFunc("GET "+root+"/nodes", listOrWatch(h, svc.ListNodes, svc.WatchNodes))
	mux.HandleFunc("PUT "+root+"/leases/{name}", put(h, svc.PutLease))
	mux.HandleFunc("GET "+root+"/leases/{name}", get(h, svc.GetLease))
	mux.HandleFunc("GET "+root+"/leases", listOrWatch(h, svc.ListLeases, svc.WatchLeases))
	mux.HandleFunc(createCSRRoute, h.createCertificateSigningRequest(svc))
	mux.HandleFunc("PUT "+root+"/certificatesigningrequests/{name}/status", put(h, svc.PutCertificateSigningRequestStatus))
	mux.HandleFunc(getCSRRoute, h.getCertificateSigningRequest(svc))
	mux.HandleFunc("GET "+root+"/certificatesigningrequests", listOrWatch(h, svc.ListCertificateSigningRequests, svc.WatchCertificateSigningRequests))
	return h.recoverPanics(h.authenticate(mux, tokens))
}

// The routes a bearer-token holder may reach.
const (
	createCSRRoute = "PUT " + root + "/certificatesigningrequests/{name}"
	getCSRRoute    = "GET " + root + "/certificatesigningrequests/{name}"
)

type handler struct {
	log *slog.Logger
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

func (h *handler) deleteMachine(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rv := api.ResourceVersion(r.URL.Query().Get("resourceVersion"))
		out, err := svc.DeleteMachine(r.Context(), r.PathValue("name"), rv)
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

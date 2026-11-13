package guest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Content types of the two responses.
const (
	contentLog  = "application/octet-stream"
	contentExec = "application/x-ndjson"
)

// Handler returns the routes of the agent's listener: the log and the exec of
// a MicroVM. A failure that can be told before the response begins is its
// status and an api.APIError; one after it cuts the log response, and ends the
// exec response with an Error line.
func Handler(svc Service, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /microvms/{name}/log", h.serveLog)
	mux.HandleFunc("POST /microvms/{name}/exec", h.exec)
	return h.recoverPanics(mux)
}

type traceKey struct{}

// recoverPanics answers 500 where a handler panics, which net/http would
// answer by closing the connection, and gives each request a trace id that its
// log lines carry. A panic of http.ErrAbortHandler is the handler's way to cut
// a response, and goes on.
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
			h.logOf(r).Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", rec)
			w.Header().Set("Content-Type", "application/json")
			e := api.NewAPIError(api.ReasonInternalError, "internal error")
			w.WriteHeader(e.Code)
			_ = json.NewEncoder(w).Encode(e)
		}()
		next.ServeHTTP(w, r)
	})
}

// logOf returns the logger of a request, which carries its trace id.
func (h *handler) logOf(r *http.Request) *slog.Logger {
	id, _ := r.Context().Value(traceKey{}).(string)
	return h.log.With("trace_id", id)
}

func newTraceID() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

type handler struct {
	svc Service
	log *slog.Logger
}

// query parses the raw query of r, refusing one that does not parse instead of
// dropping the pairs that do not.
func query(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, api.NewAPIError(api.ReasonInvalid, "the query is malformed: %v", err)
	}
	return q, nil
}

// target reads the query q that names the Machine of r, and the parameters in
// allowed.
func target(r *http.Request, q url.Values, allowed ...string) (Target, error) {
	known := map[string]bool{"bindingEpoch": true, "uid": true}
	for _, a := range allowed {
		known[a] = true
	}
	for k, v := range q {
		if !known[k] {
			return Target{}, api.NewAPIError(api.ReasonInvalid, "unknown query parameter %q", k)
		}
		if len(v) != 1 {
			return Target{}, api.NewAPIError(api.ReasonInvalid, "query parameter %q is given %d times", k, len(v))
		}
	}
	epoch, err := strconv.ParseInt(q.Get("bindingEpoch"), 10, 64)
	if err != nil {
		return Target{}, api.NewAPIError(api.ReasonInvalid, "bindingEpoch must be an integer")
	}
	uid := q.Get("uid")
	if uid == "" {
		return Target{}, api.NewAPIError(api.ReasonInvalid, "uid is required")
	}
	return Target{Name: r.PathValue("name"), Epoch: api.Epoch(epoch), UID: api.UID(uid)}, nil
}

func (h *handler) serveLog(w http.ResponseWriter, r *http.Request) {
	q, err := query(r)
	var t Target
	if err == nil {
		t, err = target(r, q, "follow", "tailBytes")
	}
	req := LogRequest{Target: t}
	if err == nil && q.Has("follow") {
		switch q.Get("follow") {
		case "true":
			req.Follow = true
		case "false":
		default:
			err = api.NewAPIError(api.ReasonInvalid, "follow must be true or false")
		}
	}
	if err == nil && q.Has("tailBytes") {
		req.TailBytes, err = strconv.ParseInt(q.Get("tailBytes"), 10, 64)
		if err != nil || req.TailBytes < 1 {
			err = api.NewAPIError(api.ReasonInvalid, "tailBytes must be an integer of at least 1")
		}
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := &logOutput{w: w, rc: http.NewResponseController(w)}
	err = h.svc.Log(r.Context(), req, out)
	if err == nil {
		return
	}
	if !out.begun {
		h.fail(w, r, err)
		return
	}
	// The status is sent: the only way left to tell the client the log is
	// incomplete is to cut the response short.
	if r.Context().Err() == nil {
		h.logOf(r).Error("log read failed after the response began", "microvm", t.Name, "error", err)
	}
	panic(http.ErrAbortHandler)
}

type execBody struct {
	Command []string `json:"command"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
	WorkingDir string `json:"workingDir"`
}

// maxExecBody bounds the body of an exec request.
const maxExecBody = 1 << 20

func readExec(r *http.Request, w http.ResponseWriter) (ExecRequest, error) {
	q, err := query(r)
	if err != nil {
		return ExecRequest{}, err
	}
	t, err := target(r, q)
	if err != nil {
		return ExecRequest{}, err
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBody))
	dec.DisallowUnknownFields()
	var b execBody
	if err := dec.Decode(&b); err != nil {
		return ExecRequest{}, api.NewAPIError(api.ReasonInvalid, "the body is not a valid exec request: %v", err)
	}
	if dec.More() {
		return ExecRequest{}, api.NewAPIError(api.ReasonInvalid, "the body holds more than one object")
	}
	if len(b.Command) == 0 || b.Command[0] == "" {
		return ExecRequest{}, api.NewAPIError(api.ReasonInvalid, "command needs a first element")
	}
	env := make(map[string]string, len(b.Env))
	for _, e := range b.Env {
		if !api.ValidEnvName(e.Name) {
			return ExecRequest{}, api.NewAPIError(api.ReasonInvalid, "env name %q is not valid", e.Name)
		}
		if _, dup := env[e.Name]; dup {
			return ExecRequest{}, api.NewAPIError(api.ReasonInvalid, "env name %q is given twice", e.Name)
		}
		env[e.Name] = e.Value
	}
	return ExecRequest{Target: t, Command: b.Command, Env: env, WorkingDir: b.WorkingDir}, nil
}

func (h *handler) exec(w http.ResponseWriter, r *http.Request) {
	req, err := readExec(r, w)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := &execOutput{w: w, rc: http.NewResponseController(w)}
	err = h.svc.Exec(r.Context(), req, out)
	if err == nil {
		return
	}
	if !out.begun {
		h.fail(w, r, err)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	h.logOf(r).Error("exec failed after the response began", "microvm", req.Name, "error", err)
	if werr := out.fail(failureOf(err)); werr != nil {
		h.logOf(r).Debug("the exec error line was not sent", "microvm", req.Name, "error", werr)
	}
}

// failureOf returns the *api.APIError a caller is answered with for err: the
// one in its chain, otherwise InternalError without the cause.
func failureOf(err error) *api.APIError {
	var e *api.APIError
	if errors.As(err, &e) {
		return e
	}
	return api.NewAPIError(api.ReasonInternalError, "the request failed")
}

// fail answers a request that failed before its response began, and logs what
// the caller is not told.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	e := failureOf(err)
	var f *failure
	if errors.As(err, &f) && f.cause != nil || e.Reason == api.ReasonInternalError {
		h.logOf(r).Warn("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Code)
	if werr := json.NewEncoder(w).Encode(e); werr != nil {
		h.logOf(r).Debug("the error response was not sent", "error", werr)
	}
}

type logOutput struct {
	w     http.ResponseWriter
	rc    *http.ResponseController
	begun bool
}

func (o *logOutput) Begin() {
	o.begun = true
	o.w.Header().Set("Content-Type", contentLog)
	o.w.WriteHeader(http.StatusOK)
	_ = o.rc.Flush()
}

func (o *logOutput) Write(data []byte) error {
	if _, err := o.w.Write(data); err != nil {
		return err
	}
	return o.rc.Flush()
}

type execOutput struct {
	w     http.ResponseWriter
	rc    *http.ResponseController
	begun bool
}

func (o *execOutput) Begin() {
	o.begun = true
	o.w.Header().Set("Content-Type", contentExec)
	o.w.WriteHeader(http.StatusOK)
	_ = o.rc.Flush()
}

func (o *execOutput) line(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := o.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return o.rc.Flush()
}

func (o *execOutput) Output(stream Stream, data []byte) error {
	return o.line(struct {
		Type   string `json:"type"`
		Stream Stream `json:"stream"`
		Data   string `json:"data"`
	}{"Output", stream, base64.StdEncoding.EncodeToString(data)})
}

var exitStatus = map[v1alpha2.ExecStatus]string{
	v1alpha2.ExecStatus_EXEC_STATUS_EXITED:      "Exited",
	v1alpha2.ExecStatus_EXEC_STATUS_CUT_SHORT:   "CutShort",
	v1alpha2.ExecStatus_EXEC_STATUS_NOT_STARTED: "NotStarted",
}

func (o *execOutput) Exit(rec *v1alpha2.ExecRecord) error {
	status, ok := exitStatus[rec.GetStatus()]
	if !ok {
		return fmt.Errorf("the exec ended in status %s", rec.GetStatus())
	}
	line := struct {
		Type       string `json:"type"`
		Status     string `json:"status"`
		ExitCode   *int32 `json:"exitCode,omitempty"`
		TermSignal *int32 `json:"termSignal,omitempty"`
		Reason     string `json:"reason,omitempty"`
	}{Type: "Exit", Status: status, Reason: rec.GetReason()}
	if rec.GetStatus() == v1alpha2.ExecStatus_EXEC_STATUS_EXITED {
		code := rec.GetExitCode()
		line.ExitCode = &code
	}
	if rec.GetTermSignal() != 0 {
		sig := rec.GetTermSignal()
		line.TermSignal = &sig
	}
	return o.line(line)
}

// fail ends the response with an Error line.
func (o *execOutput) fail(e *api.APIError) error {
	ev, err := api.NewErrorEvent(e)
	if err != nil {
		return err
	}
	return o.line(ev)
}

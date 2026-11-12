//go:build extensions

package extensions

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// extender is a scheduler extender the test serves on loopback. Its filter
// refuses the Node named refused for each MicroVM whose name starts with
// prefix, and passes every other Node and MicroVM.
type extender struct {
	URL     string
	prefix  string
	refused string

	mu     sync.Mutex
	asked  []string
	failed []string
}

func startExtender(t *testing.T, prefix, refused string) *extender {
	t.Helper()
	e := &extender{prefix: prefix, refused: refused}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /filter", e.filter)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.URL = srv.URL
	return e
}

func (e *extender) filter(w http.ResponseWriter, r *http.Request) {
	var args struct {
		MicroVM   *api.MicroVM `json:"microvm"`
		NodeNames []string     `json:"nodeNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil || args.MicroVM == nil {
		http.Error(w, "unreadable filter request", http.StatusBadRequest)
		return
	}
	name := args.MicroVM.Metadata.Name
	res := struct {
		NodeNames   []string          `json:"nodeNames"`
		FailedNodes map[string]string `json:"failedNodes"`
	}{NodeNames: []string{}, FailedNodes: map[string]string{}}
	e.mu.Lock()
	e.asked = append(e.asked, name)
	for _, n := range args.NodeNames {
		if strings.HasPrefix(name, e.prefix) && n == e.refused {
			res.FailedNodes[n] = "the extender keeps " + e.prefix + " MicroVMs off " + n
			e.failed = append(e.failed, name+"@"+n)
			continue
		}
		res.NodeNames = append(res.NodeNames, n)
	}
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// Asked returns the names of the MicroVMs the extender was sent, in order.
func (e *extender) Asked() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.asked...)
}

// Refusals returns each Node the extender refused, as "<microvm>@<node>".
func (e *extender) Refusals() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.failed...)
}

// webhook is a validating admission webhook the test serves over TLS on
// loopback. It refuses an object whose spec carries the field marker, with
// the message it was given, and allows every other.
type webhook struct {
	URL     string
	field   string
	message string

	mu   sync.Mutex
	seen []string
}

func startWebhook(t *testing.T, id identity, field, message string) *webhook {
	t.Helper()
	w := &webhook{field: field, message: message}
	pair, err := tls.LoadX509KeyPair(id.CertFile, id.KeyFile)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /validate", w.validate)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	w.URL = "https://" + ln.Addr().String() + "/validate"
	return w
}

func (w *webhook) validate(rw http.ResponseWriter, r *http.Request) {
	var review api.AdmissionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Request == nil {
		http.Error(rw, "unreadable AdmissionReview", http.StatusBadRequest)
		return
	}
	var obj struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	_ = json.Unmarshal(review.Request.Object, &obj)
	_, marked := obj.Spec[w.field]
	resp := &api.AdmissionResponse{UID: review.Request.UID, Allowed: !marked}
	w.mu.Lock()
	w.seen = append(w.seen, review.Request.Name)
	if marked {
		resp.Status = &api.AdmissionStatus{Code: http.StatusForbidden, Message: w.message}
	}
	w.mu.Unlock()
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(api.AdmissionReview{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
		Response: resp,
	})
}

// Seen returns the names of the objects the webhook was asked about, in order.
func (w *webhook) Seen() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.seen...)
}

package apiserver_test

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// admissionAPIRoot is the root of the webhook-configuration group, which the
// server is documented to serve.
const admissionAPIRoot = "/apis/admissionregistration.gaon.garam.sh/v1alpha1"

// call is what one webhook call carried.
type call struct {
	method      string
	contentType string
	path        string
	review      api.AdmissionReview
}

// webhookServer is an https admission webhook in this process, with a CA of
// its own. serve answers a call; where it is nil, the webhook answers with
// answer, which allows by default.
type webhookServer struct {
	ca     *authority
	srv    *httptest.Server
	answer func(c call) *api.AdmissionResponse
	serve  func(w http.ResponseWriter, r *http.Request, c call)

	mu    sync.Mutex
	calls []call
}

func newWebhookServer(t *testing.T) *webhookServer {
	t.Helper()
	w := &webhookServer{ca: newAuthority(t, "webhook ca")}
	certPEM, keyPEM := w.ca.issue(t, "webhook")
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	w.srv = httptest.NewUnstartedServer(http.HandlerFunc(w.handle))
	w.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	w.srv.StartTLS()
	t.Cleanup(w.srv.Close)
	return w
}

func (w *webhookServer) handle(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c := call{method: r.Method, contentType: r.Header.Get("Content-Type"), path: r.URL.Path}
	_ = json.Unmarshal(body, &c.review)
	w.mu.Lock()
	w.calls = append(w.calls, c)
	w.mu.Unlock()
	if w.serve != nil {
		w.serve(rw, r, c)
		return
	}
	resp := allow(c)
	if w.answer != nil {
		resp = w.answer(c)
	}
	reply(rw, resp)
}

func reply(rw http.ResponseWriter, resp *api.AdmissionResponse) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(api.AdmissionReview{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
		Response: resp,
	})
}

func allow(c call) *api.AdmissionResponse {
	return &api.AdmissionResponse{UID: c.review.Request.UID, Allowed: true}
}

func patch(c call, ops string) *api.AdmissionResponse {
	resp := allow(c)
	resp.PatchType, resp.Patch = api.PatchTypeJSONPatch, []byte(ops)
	return resp
}

func deny(c call, code int, message string) *api.AdmissionResponse {
	return &api.AdmissionResponse{UID: c.review.Request.UID, Status: &api.AdmissionStatus{Code: code, Message: message}}
}

// received returns the calls so far.
func (w *webhookServer) received() []call {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]call(nil), w.calls...)
}

// hook returns a webhook of the server named name for every operation, group
// and version, and the given resources, at path.
func (w *webhookServer) hook(name, path string, resources ...string) api.Webhook {
	return api.Webhook{
		Name:         name,
		ClientConfig: api.WebhookClientConfig{URL: w.srv.URL + path, CABundle: w.ca.pem},
		Rules: []api.WebhookRule{{
			Operations:  []api.Operation{api.OperationAll},
			APIGroups:   []string{"*"},
			APIVersions: []string{"*"},
			Resources:   resources,
		}},
	}
}

// everything is the webhook of w that every write to a resource matches.
func (w *webhookServer) everything() api.Webhook {
	return w.hook("all.hooks.example", "/", "*", "*/*")
}

// admission is the client of the webhook-configuration group on the plain HTTP
// server of e, which authorizes nobody.
func (e *env) admission() *api.Client {
	return api.NewClient(e.base+admissionAPIRoot, nil)
}

// mutating registers a mutating configuration of the webhooks and removes it
// when the test ends.
func (e *env) mutating(t *testing.T, name string, hooks ...api.Webhook) {
	t.Helper()
	c := &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: name},
		Webhooks: hooks,
	}
	created, err := e.admission().PutMutatingWebhookConfiguration(ctx(t), c)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = e.admission().DeleteMutatingWebhookConfiguration(ctx(t), name, created.Metadata.ResourceVersion)
	})
}

// validating registers a validating configuration of the webhooks and removes
// it when the test ends.
func (e *env) validating(t *testing.T, name string, hooks ...api.Webhook) {
	t.Helper()
	c := &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: name},
		Webhooks: hooks,
	}
	created, err := e.admission().PutValidatingWebhookConfiguration(ctx(t), c)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = e.admission().DeleteValidatingWebhookConfiguration(ctx(t), name, created.Metadata.ResourceVersion)
	})
}

const setImage = `[{"op":"replace","path":"/spec/image","value":"patched"}]`

// admissionClient returns the client of the webhook-configuration group for
// the caller whose certificate has the subject w.
func (c *cluster) admissionClient(t *testing.T, addr string, w who) *api.Client {
	t.Helper()
	certPEM, keyPEM := c.clientCA.issueFor(t, w)
	hc, err := api.NewTLSClient(c.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	hc.Timeout = 10 * time.Second
	return api.NewClient("https://"+addr+admissionAPIRoot, hc)
}

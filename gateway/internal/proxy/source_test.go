package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// fakeAPI serves the list and the watch of the kinds the proxy reads, under
// the token it holds, and records the label selector each kind was asked with.
type fakeAPI struct {
	*httptest.Server

	mu        sync.Mutex
	token     string
	down      bool
	lists     map[string]any
	selectors map[string]map[string]bool
	events    map[string]chan api.WatchEvent
}

var kinds = []string{"gateways", "httproutes", "tlsroutes", "services", "endpoints", "secrets"}

func newFakeAPI(t *testing.T, token string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{token: token, lists: map[string]any{}, selectors: map[string]map[string]bool{}, events: map[string]chan api.WatchEvent{}}
	for _, k := range kinds {
		f.selectors[k] = map[string]bool{}
		f.events[k] = make(chan api.WatchEvent, 16)
	}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down, token := f.down, f.token
	f.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kind := path.Base(r.URL.Path)
	f.mu.Lock()
	if sel := r.URL.Query().Get(api.LabelSelectorParam); sel != "" {
		f.selectors[kind][sel] = true
	} else {
		f.selectors[kind][""] = true
	}
	list := f.lists[kind]
	f.mu.Unlock()
	if r.URL.Query().Get("watch") == "true" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		enc := json.NewEncoder(w)
		for {
			select {
			case ev := <-f.events[kind]:
				if enc.Encode(ev) != nil {
					return
				}
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
	if list == nil {
		list = emptyList(kind)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func emptyList(kind string) any {
	meta := api.ListMeta{ResourceVersion: "1"}
	switch kind {
	case "services":
		return api.ServiceList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceList}, Metadata: meta}
	case "endpoints":
		return api.EndpointsList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpointsList}, Metadata: meta}
	case "secrets":
		return api.SecretList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecretList}, Metadata: meta}
	}
	return api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: gateway.APIVersion, Kind: "List"}, Metadata: meta, Items: []api.CustomObject{}}
}

// set replaces what a list of the kind returns.
func (f *fakeAPI) set(kind string, list any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists[kind] = list
}

// setObjects makes the lists hold o.
func (f *fakeAPI) setObjects(o proxy.Objects) {
	meta := api.ListMeta{ResourceVersion: "1"}
	f.set("gateways", api.CustomObjectList{Metadata: meta, Items: o.Gateways})
	f.set("httproutes", api.CustomObjectList{Metadata: meta, Items: o.HTTPRoutes})
	f.set("tlsroutes", api.CustomObjectList{Metadata: meta, Items: o.TLSRoutes})
	f.set("services", api.ServiceList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceList}, Metadata: meta, Items: o.Services})
	f.set("endpoints", api.EndpointsList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpointsList}, Metadata: meta, Items: o.Endpoints})
	f.set("secrets", api.SecretList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecretList}, Metadata: meta, Items: o.Secrets})
}

func push[T any](t *testing.T, f *fakeAPI, kind string, typ api.EventType, obj T) {
	t.Helper()
	ev, err := api.NewEvent(typ, &obj)
	require.NoError(t, err)
	f.events[kind] <- ev
}

// setToken changes the token requests must carry and ends the watches in
// progress, so the proxy lists again.
func (f *fakeAPI) setToken(token string) {
	f.mu.Lock()
	f.token = token
	f.mu.Unlock()
	f.CloseClientConnections()
}

func (f *fakeAPI) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
	if down {
		f.CloseClientConnections()
	}
}

func (f *fakeAPI) selectorsOf(kind string) map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for k, v := range f.selectors[kind] {
		out[k] = v
	}
	return out
}

// follow starts a Source of the harness's proxy on the fake API, reading the
// token from a file the test can rewrite, and stops it when the test ends.
func follow(t *testing.T, h *harness, f *fakeAPI, token string) (tokenFile string) {
	t.Helper()
	tokenFile = filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(token+"\n"), 0o600))
	src := &proxy.Source{
		Server:    f.URL,
		HTTP:      f.Client(),
		TokenFile: tokenFile,
		Retry:     20 * time.Millisecond,
		Log:       slog.New(slog.DiscardHandler),
		Proxy:     h.Proxy,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = src.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the source did not stop")
		}
	})
	return tokenFile
}

// try sends one request and returns the status and the body, or -1 when the
// proxy does not answer.
func try(addr, path string) (int, string) {
	resp, err := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Get("http://" + addr + path)
	if err != nil {
		return -1, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// servedWithin waits for a request to the listener on port 80 to answer as
// wanted. The listener opens as soon as the Gateway is listed, so the address
// is looked up on each try.
func servedWithin(t *testing.T, h *harness, status int, body string) {
	t.Helper()
	assert.Eventually(t, func() bool {
		if !h.opened(80) {
			return false
		}
		h.mu.Lock()
		addr := h.lns[80].Addr().String()
		h.mu.Unlock()
		s, b := try(addr, "/")
		return s == status && (body == "" || b == body)
	}, 5*time.Second, 20*time.Millisecond, "want status %d body %q", status, body)
}

func sourceObjects(t *testing.T, accepted bool, backend string) proxy.Objects {
	t.Helper()
	o := objects(t, gatewayOf(t, httpListener("web", 80, "")),
		[]api.CustomObject{httpRoute(t, "app", nil, accepted, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"}))},
		map[string][]string{"a": {backend}})
	return o
}

func TestSource_ServesWhatTheAPIHoldsUnderTheToken(t *testing.T) {
	f := newFakeAPI(t, "tok1")
	f.setObjects(sourceObjects(t, true, serving(t, "from the API")))
	h := newHarness(t, proxy.Config{})
	follow(t, h, f, "tok1")
	servedWithin(t, h, http.StatusOK, "from the API")
	require.Eventually(t, func() bool {
		for _, kind := range kinds {
			if len(f.selectorsOf(kind)) == 0 {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond, "setup: every kind was asked for")

	assert.Equal(t, map[string]bool{gateway.CertificateLabel + "=" + gateway.CertificateLabelValue: true}, f.selectorsOf("secrets"),
		"Secrets are asked for by the certificate label only")
	for _, kind := range []string{"gateways", "httproutes", "tlsroutes", "services", "endpoints"} {
		assert.Equal(t, map[string]bool{"": true}, f.selectorsOf(kind), kind+" carries no selector")
	}
}

func TestSource_RouteStatusAndEndpointsAreFollowed(t *testing.T) {
	f := newFakeAPI(t, "tok1")
	o := sourceObjects(t, true, serving(t, "up"))
	f.setObjects(o)
	h := newHarness(t, proxy.Config{})
	follow(t, h, f, "tok1")
	servedWithin(t, h, http.StatusOK, "up")

	t.Run("a route no longer Accepted stops being served", func(t *testing.T) {
		push(t, f, "httproutes", api.EventModified, httpRoute(t, "app", nil, false, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"})))
		servedWithin(t, h, http.StatusNotFound, "")
	})
	t.Run("control: the same route Accepted again is served again", func(t *testing.T) {
		push(t, f, "httproutes", api.EventModified, httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"})))
		servedWithin(t, h, http.StatusOK, "up")
	})
	t.Run("Endpoints that lose their addresses answer 503", func(t *testing.T) {
		_, eps := backendOf(t, "a")
		push(t, f, "endpoints", api.EventModified, eps)
		servedWithin(t, h, http.StatusServiceUnavailable, "")
	})
	t.Run("Endpoints that gain an address are served", func(t *testing.T) {
		_, eps := backendOf(t, "a", serving(t, "new"))
		push(t, f, "endpoints", api.EventModified, eps)
		servedWithin(t, h, http.StatusOK, "new")
	})
}

func TestSource_TokenFileIsReadAgainWhenTheProxyLists(t *testing.T) {
	f := newFakeAPI(t, "tok1")
	f.setObjects(sourceObjects(t, true, serving(t, "old")))
	h := newHarness(t, proxy.Config{})
	tokenFile := follow(t, h, f, "tok1")
	servedWithin(t, h, http.StatusOK, "old")

	f.setObjects(sourceObjects(t, true, serving(t, "new")))
	require.NoError(t, os.WriteFile(tokenFile, []byte("tok2\n"), 0o600))
	f.setToken("tok2")
	servedWithin(t, h, http.StatusOK, "new")
}

func TestSource_LastStateIsServedWhileTheAPIIsDown(t *testing.T) {
	f := newFakeAPI(t, "tok1")
	f.setObjects(sourceObjects(t, true, serving(t, "old")))
	h := newHarness(t, proxy.Config{})
	follow(t, h, f, "tok1")
	servedWithin(t, h, http.StatusOK, "old")

	f.setDown(true)
	time.Sleep(200 * time.Millisecond)
	servedWithin(t, h, http.StatusOK, "old")

	f.setObjects(sourceObjects(t, true, serving(t, "new")))
	f.setDown(false)
	servedWithin(t, h, http.StatusOK, "new")
}

func TestSource_CertificateSecretIsLoadedFromTheLabelledList(t *testing.T) {
	pair := newKeyPair(t, "app.example.com")
	f := newFakeAPI(t, "tok1")
	o := httpsObjects(t, pair.secret("app-cert"))
	f.setObjects(o)
	h := newHarness(t, proxy.Config{})
	follow(t, h, f, "tok1")
	require.Eventually(t, func() bool {
		if !h.opened(443) {
			return false
		}
		h.mu.Lock()
		addr := h.lns[443].Addr().String()
		h.mu.Unlock()
		resp, err := tlsClient(addr, pair.pool()).Get("https://app.example.com/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond)

	replacement := newKeyPair(t, "app.example.com")
	push(t, f, "secrets", api.EventModified, replacement.secret("app-cert"))
	require.Eventually(t, func() bool {
		h.mu.Lock()
		addr := h.lns[443].Addr().String()
		h.mu.Unlock()
		c, err := servedBy(t, addr, "app.example.com")
		if err != nil {
			return false
		}
		defer c.Close()
		return replacement.cert.Equal(c.ConnectionState().PeerCertificates[0])
	}, 5*time.Second, 20*time.Millisecond, "a Secret that changes is served without a restart")
}

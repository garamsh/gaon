package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

// fakeAPI serves what the gateway service uses: the definitions, the three
// gateway kinds, the Deployments and the Services. It stores an object as sent,
// with the compare-and-swap of the real server, keeps a status a spec write does
// not carry, and plays the Service allocator: a created Service holds the
// address it asked for, or one of its own, unless the test refuses the address.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu     sync.Mutex
	store  map[string]map[string]map[string]any
	nextRV int
	pool   int
	// refuse maps a loadBalancerIP to the reason the server refuses a Service that asks for it.
	refuse map[string]api.Reason
	// puts counts the writes of a spec, and statusPuts those of a status, by "<plural>/<name>".
	puts       map[string]int
	statusPuts map[string]int
	// lists counts the lists of Gateways, which one sweep makes once.
	lists int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, store: map[string]map[string]map[string]any{}, nextRV: 100, pool: 100,
		refuse: map[string]api.Reason{}, puts: map[string]int{}, statusPuts: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) rv() string {
	f.nextRV++
	return fmt.Sprint(f.nextRV)
}

func (f *fakeAPI) kind(plural string) map[string]map[string]any {
	if f.store[plural] == nil {
		f.store[plural] = map[string]map[string]any{}
	}
	return f.store[plural]
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, reason api.Reason) {
	e := api.NewAPIError(reason, "object")
	writeJSON(w, e.Code, e)
}

func metadata(o map[string]any) map[string]any {
	m, _ := o["metadata"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		o["metadata"] = m
	}
	return m
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/apis/"), "/")
	if len(parts) < 3 {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
		return
	}
	plural, name, sub := parts[2], "", ""
	if len(parts) > 3 {
		name = parts[3]
	}
	if len(parts) > 4 {
		sub = parts[4]
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	objects := f.kind(plural)
	switch {
	case r.Method == http.MethodGet && name == "":
		if plural == "gateways" {
			f.lists++
		}
		items := []any{}
		for _, o := range objects {
			items = append(items, o)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "metadata": map[string]any{"resourceVersion": "1"}})
	case r.Method == http.MethodGet:
		o, ok := objects[name]
		if !ok {
			fail(w, api.ReasonNotFound)
			return
		}
		writeJSON(w, http.StatusOK, o)
	case r.Method == http.MethodPut && sub == "status":
		f.putStatus(w, r, plural, name)
	case r.Method == http.MethodPut:
		f.put(w, r, plural, name)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}

func decode(r *http.Request) (map[string]any, bool) {
	var o map[string]any
	return o, json.NewDecoder(r.Body).Decode(&o) == nil
}

func (f *fakeAPI) put(w http.ResponseWriter, r *http.Request, plural, name string) {
	in, ok := decode(r)
	if !ok {
		fail(w, api.ReasonInvalid)
		return
	}
	objects := f.kind(plural)
	old, exists := objects[name]
	sent, _ := metadata(in)["resourceVersion"].(string)
	switch {
	case !exists && sent != "":
		fail(w, api.ReasonNotFound)
		return
	case exists && sent == "":
		fail(w, api.ReasonAlreadyExists)
		return
	case exists && sent != metadata(old)["resourceVersion"]:
		fail(w, api.ReasonConflict)
		return
	}
	if exists {
		if status, ok := old["status"]; ok {
			in["status"] = status
		}
	} else {
		metadata(in)["uid"] = string(api.NewUID())
		if plural == "services" {
			if !f.allocate(w, in) {
				return
			}
		}
	}
	metadata(in)["resourceVersion"] = f.rv()
	objects[name] = in
	f.puts[plural+"/"+name]++
	writeJSON(w, http.StatusOK, in)
}

// allocate gives a new Service its LAN address, or refuses it.
func (f *fakeAPI) allocate(w http.ResponseWriter, svc map[string]any) bool {
	spec, _ := svc["spec"].(map[string]any)
	ip, _ := spec["loadBalancerIP"].(string)
	if reason, refused := f.refuse[ip]; refused {
		fail(w, reason)
		return false
	}
	if ip == "" {
		f.pool++
		ip = fmt.Sprintf("192.168.1.%d", f.pool)
	}
	svc["status"] = map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": ip}}}}
	return true
}

func (f *fakeAPI) putStatus(w http.ResponseWriter, r *http.Request, plural, name string) {
	in, ok := decode(r)
	old, exists := f.kind(plural)[name]
	if !ok || !exists {
		fail(w, api.ReasonNotFound)
		return
	}
	if metadata(in)["resourceVersion"] != metadata(old)["resourceVersion"] {
		fail(w, api.ReasonConflict)
		return
	}
	old["status"] = in["status"]
	metadata(old)["resourceVersion"] = f.rv()
	f.statusPuts[plural+"/"+name]++
	writeJSON(w, http.StatusOK, old)
}

// seed stores an object of the kind, as a user created it.
func (f *fakeAPI) seed(plural, name string, obj map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	metadata(obj)["name"] = name
	metadata(obj)["resourceVersion"] = f.rv()
	if metadata(obj)["uid"] == nil {
		metadata(obj)["uid"] = string(api.NewUID())
	}
	f.kind(plural)[name] = obj
}

func parse(t *testing.T, s string) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v), s)
	return v
}

// addGateway stores a Gateway with the uid and the spec JSON given.
func (f *fakeAPI) addGateway(name, uid, spec string) {
	f.seed("gateways", name, map[string]any{
		"apiVersion": gateway.APIVersion, "kind": gateway.KindGateway,
		"metadata": map[string]any{"uid": uid}, "spec": parse(f.t, spec),
	})
}

// addRoute stores a route of the kind, "httproutes" or "tlsroutes", with the spec JSON given.
func (f *fakeAPI) addRoute(plural, name, spec string) {
	kind := gateway.KindHTTPRoute
	if plural == "tlsroutes" {
		kind = gateway.KindTLSRoute
	}
	f.seed(plural, name, map[string]any{"apiVersion": gateway.APIVersion, "kind": kind, "spec": parse(f.t, spec)})
}

// addService stores a Service with the ports numbers given, as a backend.
func (f *fakeAPI) addService(name string, ports ...int32) {
	var ps []any
	for _, p := range ports {
		ps = append(ps, map[string]any{"name": fmt.Sprint("p", p), "protocol": "TCP", "port": p})
	}
	f.seed("services", name, map[string]any{
		"apiVersion": api.GroupVersion, "kind": api.KindService,
		"spec": map[string]any{"ports": ps},
	})
}

// setGatewaySpec replaces the spec of a stored Gateway, as a user's edit does.
func (f *fakeAPI) setGatewaySpec(name, spec string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.kind("gateways")[name]
	o["spec"] = parse(f.t, spec)
	metadata(o)["resourceVersion"] = f.rv()
}

func (f *fakeAPI) refuseAddress(ip string, reason api.Reason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse[ip] = reason
}

// object returns the stored object of the kind, decoded.
func (f *fakeAPI) object(plural, name string) (api.CustomObject, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.kind(plural)[name]
	if !ok {
		return api.CustomObject{}, false
	}
	raw, err := json.Marshal(o)
	require.NoError(f.t, err)
	var out api.CustomObject
	require.NoError(f.t, json.Unmarshal(raw, &out))
	return out, true
}

func (f *fakeAPI) service(name string) (api.Service, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.kind("services")[name]
	if !ok {
		return api.Service{}, false
	}
	raw, err := json.Marshal(o)
	require.NoError(f.t, err)
	var out api.Service
	require.NoError(f.t, json.Unmarshal(raw, &out))
	return out, true
}

func (f *fakeAPI) definition(name string) (api.CustomResourceDefinition, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.kind("customresourcedefinitions")[name]
	if !ok {
		return api.CustomResourceDefinition{}, false
	}
	raw, err := json.Marshal(o)
	require.NoError(f.t, err)
	var out api.CustomResourceDefinition
	require.NoError(f.t, json.Unmarshal(raw, &out))
	return out, true
}

func (f *fakeAPI) gatewayStatus(name string) (gateway.GatewayStatus, bool) {
	o, ok := f.object("gateways", name)
	var s gateway.GatewayStatus
	if ok && len(o.Status) > 0 {
		require.NoError(f.t, json.Unmarshal(o.Status, &s))
	}
	return s, ok && len(o.Status) > 0
}

func (f *fakeAPI) routeStatus(plural, name string) (gateway.RouteStatus, bool) {
	o, ok := f.object(plural, name)
	var s gateway.RouteStatus
	if ok && len(o.Status) > 0 {
		require.NoError(f.t, json.Unmarshal(o.Status, &s))
	}
	return s, ok && len(o.Status) > 0
}

func (f *fakeAPI) count(m map[string]int, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[key]
}

func (f *fakeAPI) putsOf(key string) int       { return f.count(f.puts, key) }
func (f *fakeAPI) statusPutsOf(key string) int { return f.count(f.statusPuts, key) }

func (f *fakeAPI) sweeps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

// proxy is the proxy configuration the tests give the service.
var proxy = gateway.ProxyConfig{Image: "registry.test/gaon/gateway-proxy@sha256:abc", Server: "https://api.test:8443", CAConfigMap: "gaon-gateway-ca", ServiceAccount: "gaon-gateway-proxy"}

// start runs the service against f until the test ends, sweeping every 10ms.
func (f *fakeAPI) start(t *testing.T) {
	t.Helper()
	types := []api.CustomResourceType{gateway.GatewayType, gateway.HTTPRouteType, gateway.TLSRouteType, gateway.DeploymentType}
	var clients [4]*api.CustomResourceClient
	for i, typ := range types {
		c, err := api.NewCustomResourceClient(f.server.URL, nil, typ)
		require.NoError(t, err)
		clients[i] = c
	}
	definitions := api.NewClient(f.server.URL+"/apis/"+api.APIExtensionsGroupVersion, nil)
	services := api.NewClient(f.server.URL+"/apis/"+api.GroupVersion, nil)
	svc := gateway.NewService(definitions, clients[0], clients[1], clients[2], clients[3], services,
		gateway.Config{Interval: 10 * time.Millisecond, Proxy: proxy}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
}

// settle waits until the service has swept twice more, so that what it was going
// to write has been written.
func (f *fakeAPI) settle(t *testing.T) {
	t.Helper()
	before := f.sweeps()
	require.Eventually(t, func() bool { return f.sweeps() >= before+3 }, 5*time.Second, 5*time.Millisecond, "the service sweeps")
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, msg)
}

// cond returns the condition of the type, or one with an empty Type.
func cond(conds []api.Condition, typ string) api.Condition {
	for _, c := range conds {
		if c.Type == typ {
			return c
		}
	}
	return api.Condition{}
}

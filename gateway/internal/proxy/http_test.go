package proxy_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// get sends one request to the proxy at addr with the Host and the headers
// given, and returns the status and the body.
func get(t *testing.T, addr, host, path string, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	require.NoError(t, err)
	if host != "" {
		req.Host = host
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// serveHTTP applies a Gateway with one HTTP listener on port 80 and returns the
// address of the listener.
func serveHTTP(t *testing.T, h *harness, routes []api.CustomObject, backends map[string][]string) string {
	t.Helper()
	h.Apply(objects(t, gatewayOf(t, httpListener("web", 80, "")), routes, backends))
	return h.addr(t, 80)
}

// echoing starts a backend that answers with what it received: the forwarded
// headers, the Host and the request URI.
func echoing(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "xff=%s proto=%s host=%s uri=%s", r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto"), r.Host, r.RequestURI)
	}))
	t.Cleanup(s.Close)
	return hostPort(t, s)
}

func ptr(s string) *string { return &s }

func TestHTTP_PrefixMatchesOnPathElements(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/abc"}, backend: "a"}))},
		map[string][]string{"a": {serving(t, "a")}})
	for _, c := range []struct {
		path   string
		status int
	}{
		{"/abc", 200},
		{"/abc/", 200},
		{"/abc/d", 200},
		{"/abcd", 404},
		{"/ab", 404},
	} {
		t.Run(c.path, func(t *testing.T) {
			status, _ := get(t, addr, "", c.path)
			assert.Equal(t, c.status, status)
		})
	}
}

func TestHTTP_ExactHostnameWinsOverWildcard(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h, []api.CustomObject{
		httpRoute(t, "a-wild", []string{"*.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "wild"})),
		httpRoute(t, "b-exact", []string{"app.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "exact"})),
	}, map[string][]string{"wild": {serving(t, "wild")}, "exact": {serving(t, "exact")}})
	for _, c := range []struct {
		host   string
		status int
		body   string
	}{
		{"app.example.com", 200, "exact"},
		{"APP.example.com:80", 200, "exact"},
		{"other.example.com", 200, "wild"},
		{"example.org", 404, ""},
	} {
		t.Run(c.host, func(t *testing.T) {
			status, body := get(t, addr, c.host, "/")
			assert.Equal(t, c.status, status)
			if c.status == 200 {
				assert.Equal(t, c.body, body)
			}
		})
	}
}

func TestHTTP_LongestPrefixWins(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h, []api.CustomObject{httpRoute(t, "app", nil, true,
		rule(t, ruleTo{prefixes: []string{"/"}, backend: "root"}),
		rule(t, ruleTo{prefixes: []string{"/api"}, backend: "api"}),
		rule(t, ruleTo{prefixes: []string{"/api/v1"}, backend: "v1"}),
	)}, map[string][]string{"root": {serving(t, "root")}, "api": {serving(t, "api")}, "v1": {serving(t, "v1")}})
	for path, want := range map[string]string{"/x": "root", "/api": "api", "/api/x": "api", "/api/v1/x": "v1", "/apix": "root"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, addr, "", path)
			assert.Equal(t, want, body)
		})
	}
}

func TestHTTP_HostAndPathAreForwardedUnmodified(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", []string{"app.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/a"}, backend: "echo"}))},
		map[string][]string{"echo": {echoing(t)}})
	_, body := get(t, addr, "app.example.com:8080", "/a/b%2Fc?x=1&y=%20")
	assert.Contains(t, body, "host=app.example.com:8080 ")
	assert.Contains(t, body, "uri=/a/b%2Fc?x=1&y=%20")
}

func TestHTTP_ReplacePrefixMatch(t *testing.T) {
	for _, c := range []struct {
		name    string
		rewrite *string
		path    string
		want    string
	}{
		{"the prefix is removed", ptr("/"), "/api/users?x=1", "uri=/users?x=1"},
		{"the whole prefix path becomes the root", ptr("/"), "/api", "uri=/"},
		{"the prefix is replaced", ptr("/v2"), "/api/users", "uri=/v2/users"},
		{"a trailing slash of the replacement is dropped", ptr("/v2/"), "/api/users", "uri=/v2/users"},
		{"without the filter the path is unmodified", nil, "/api/users", "uri=/api/users"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, proxy.Config{})
			addr := serveHTTP(t, h,
				[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/api"}, rewrite: c.rewrite, backend: "echo"}))},
				map[string][]string{"echo": {echoing(t)}})
			_, body := get(t, addr, "app.example.com", c.path)
			assert.Contains(t, body, c.want)
			assert.Contains(t, body, "host=app.example.com ", "the Host is never rewritten")
		})
	}
}

func TestHTTP_ForwardedHeaders(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "echo"}))},
		map[string][]string{"echo": {echoing(t)}})
	t.Run("the client address is appended and the protocol is replaced", func(t *testing.T) {
		_, body := get(t, addr, "", "/", "X-Forwarded-For", "10.0.0.9", "X-Forwarded-Proto", "https")
		assert.Contains(t, body, "xff=10.0.0.9, 127.0.0.1 ")
		assert.Contains(t, body, "proto=http ")
	})
	t.Run("a request without the headers gets them", func(t *testing.T) {
		_, body := get(t, addr, "", "/")
		assert.Contains(t, body, "xff=127.0.0.1 ")
		assert.Contains(t, body, "proto=http ")
	})
	t.Run("a client cannot strip them by naming them in Connection", func(t *testing.T) {
		_, body := get(t, addr, "", "/", "Connection", "X-Forwarded-Proto, X-Forwarded-For", "X-Forwarded-For", "10.0.0.9")
		assert.Contains(t, body, "proto=http ")
		assert.Contains(t, body, "xff=10.0.0.9, 127.0.0.1 ", "the peer is appended to what the client sent")
	})
	t.Run("control: Connection naming another header leaves them as they are", func(t *testing.T) {
		_, body := get(t, addr, "", "/", "Connection", "X-Other", "X-Forwarded-For", "10.0.0.9")
		assert.Contains(t, body, "proto=http ")
		assert.Contains(t, body, "xff=10.0.0.9, 127.0.0.1 ")
	})
}

func TestHTTP_NoRouteIs404AndNoReadyAddressIs503(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	routes := []api.CustomObject{
		httpRoute(t, "app", []string{"app.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "up"})),
		httpRoute(t, "down", []string{"down.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "idle"})),
		httpRoute(t, "gone", []string{"gone.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "missing"})),
	}
	addr := serveHTTP(t, h, routes, map[string][]string{"up": {serving(t, "up")}, "idle": nil})
	for _, c := range []struct {
		host   string
		status int
	}{
		{"app.example.com", http.StatusOK},
		{"down.example.com", http.StatusServiceUnavailable},
		{"gone.example.com", http.StatusServiceUnavailable},
		{"other.example.com", http.StatusNotFound},
	} {
		t.Run(c.host, func(t *testing.T) {
			status, _ := get(t, addr, c.host, "/")
			assert.Equal(t, c.status, status)
		})
	}
}

func TestHTTP_UnreachableBackendIs502(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadAddr := hostPort(t, dead)
	dead.Close()
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h, []api.CustomObject{
		httpRoute(t, "dead", []string{"dead.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "dead"})),
		httpRoute(t, "live", []string{"live.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "live"})),
	}, map[string][]string{"dead": {deadAddr}, "live": {serving(t, "live")}})
	status, _ := get(t, addr, "dead.example.com", "/")
	assert.Equal(t, http.StatusBadGateway, status)
	status, _ = get(t, addr, "live.example.com", "/")
	assert.Equal(t, http.StatusOK, status)
}

func TestHTTP_AddressesAreTakenInTurn(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "pool"}))},
		map[string][]string{"pool": {serving(t, "one"), serving(t, "two")}})
	seen := map[string]int{}
	for range 6 {
		_, body := get(t, addr, "", "/")
		seen[body]++
	}
	assert.Equal(t, map[string]int{"one": 3, "two": 3}, seen)
}

func TestHTTP_OnlyAcceptedRoutesAreServed(t *testing.T) {
	r := rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"})
	for _, c := range []struct {
		name     string
		accepted bool
		status   int
	}{
		{"an Accepted route is served", true, http.StatusOK},
		{"a route that is not Accepted is not", false, http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, proxy.Config{})
			addr := serveHTTP(t, h, []api.CustomObject{httpRoute(t, "app", nil, c.accepted, r)}, map[string][]string{"a": {serving(t, "a")}})
			status, _ := get(t, addr, "", "/")
			assert.Equal(t, c.status, status)
		})
	}
	t.Run("a route with no status is not served", func(t *testing.T) {
		h := newHarness(t, proxy.Config{})
		route := httpRoute(t, "app", nil, true, r)
		route.Status = nil
		addr := serveHTTP(t, h, []api.CustomObject{route}, map[string][]string{"a": {serving(t, "a")}})
		status, _ := get(t, addr, "", "/")
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestHTTP_ARouteIsServedOnlyOnTheListenersItAttachesTo(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	route := httpRoute(t, "app", []string{"app.example.com"}, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"}))
	h.Apply(objects(t, gatewayOf(t,
		httpListener("app", 80, "app.example.com"),
		httpListener("other", 81, "other.example.com"),
	), []api.CustomObject{route}, map[string][]string{"a": {serving(t, "a")}}))
	status, _ := get(t, h.addr(t, 80), "app.example.com", "/")
	assert.Equal(t, http.StatusOK, status)
	status, _ = get(t, h.addr(t, 81), "app.example.com", "/")
	assert.Equal(t, http.StatusNotFound, status, "the listener of another hostname does not serve the route")
}

func TestHTTP_ARouteIsServedOnlyOnTheListenerItsSectionNameNames(t *testing.T) {
	for _, c := range []struct {
		name    string
		section string
		onWeb   int
		onAdmin int
	}{
		{"the named listener serves it", "web", http.StatusOK, http.StatusNotFound},
		{"control: with no section name every listener serves it", "", http.StatusOK, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, proxy.Config{})
			ref := gateway.ParentRef{Name: gwName, SectionName: c.section}
			route := customObject(t, gateway.KindHTTPRoute, "app",
				gateway.RouteSpec{ParentRefs: []gateway.ParentRef{ref}, Rules: []gateway.Rule{rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"})}},
				gateway.RouteStatus{Parents: []gateway.ParentStatus{{
					ParentRef:  ref,
					Conditions: []api.Condition{{Type: gateway.CondAccepted, Status: api.ConditionTrue}},
				}}})
			h.Apply(objects(t, gatewayOf(t, httpListener("web", 80, ""), httpListener("admin", 81, "")),
				[]api.CustomObject{route}, map[string][]string{"a": {serving(t, "a")}}))
			status, _ := get(t, h.addr(t, 80), "x.example.com", "/")
			assert.Equal(t, c.onWeb, status)
			status, _ = get(t, h.addr(t, 81), "x.example.com", "/")
			assert.Equal(t, c.onAdmin, status)
		})
	}
}

func TestHTTP_ResponseIsStreamedNotBuffered(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len("first")+len("second")))
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("second"))
	}))
	t.Cleanup(backend.Close)
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "s"}))},
		map[string][]string{"s": {hostPort(t, backend)}})

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	first := make([]byte, len("first"))
	_, err = io.ReadFull(resp.Body, first)
	require.NoError(t, err, "the first chunk arrives while the backend still holds the rest")
	assert.Equal(t, "first", string(first))
	close(release)
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "second", string(rest))
}

func TestHTTP_UpgradedConnectionIsCopiedBothWays(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "no upgrade", http.StatusBadRequest)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(c, rw.Reader)
	}))
	t.Cleanup(backend.Close)
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "ws"}))},
		map[string][]string{"ws": {hostPort(t, backend)}})

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	for _, msg := range []string{"ping", "pong"} {
		_, err = io.WriteString(conn, msg)
		require.NoError(t, err)
		got := make([]byte, len(msg))
		_, err = io.ReadFull(br, got)
		require.NoError(t, err)
		assert.Equal(t, msg, string(got))
	}
}

func TestHTTP_ListenerWithHostnameServesItsHostnameOnly(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	h.Apply(objects(t, gatewayOf(t, gateway.Listener{Name: "web", Port: 80, Protocol: gateway.ProtocolHTTP, Hostname: "*.example.com"}),
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"}))},
		map[string][]string{"a": {serving(t, "a")}}))
	addr := h.addr(t, 80)
	status, _ := get(t, addr, "x.example.com", "/")
	assert.Equal(t, http.StatusOK, status)
	status, _ = get(t, addr, "example.org", "/")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestHTTP_LongerWildcardBeatsShorter(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	h.Apply(objects(t, gatewayOf(t, httpListener("wide", 80, "*.example.com"), httpListener("narrow", 80, "*.a.example.com")),
		[]api.CustomObject{
			routeOn(t, "z-wide", "wide", rule(t, ruleTo{prefixes: []string{"/"}, backend: "w"})),
			routeOn(t, "a-narrow", "narrow", rule(t, ruleTo{prefixes: []string{"/"}, backend: "n"})),
		}, map[string][]string{"w": {serving(t, "wide")}, "n": {serving(t, "narrow")}}))
	addr := h.addr(t, 80)
	for _, c := range []struct{ host, body string }{
		{"x.a.example.com", "narrow"},
		{"x.example.com", "wide"},
	} {
		t.Run(c.host, func(t *testing.T) {
			status, body := get(t, addr, c.host, "/")
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(t, c.body, body)
		})
	}
}

func TestHTTP_TieGoesToTheRouteWhoseNameSortsFirst(t *testing.T) {
	// The route that sorts last is listed first, so the order of the list does not decide.
	for _, c := range []struct{ name, aBackend, bBackend, want string }{
		{"a-route goes to one", "one", "two", "one"},
		{"control: a-route goes to two", "two", "one", "two"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, proxy.Config{})
			h.Apply(objects(t, gatewayOf(t, httpListener("web", 80, "")),
				[]api.CustomObject{
					httpRoute(t, "b-route", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: c.bBackend})),
					httpRoute(t, "a-route", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: c.aBackend})),
				}, map[string][]string{"one": {serving(t, "one")}, "two": {serving(t, "two")}}))
			_, body := get(t, h.addr(t, 80), "", "/")
			assert.Equal(t, c.want, body)
		})
	}
}

func TestHTTP_OnlyTheAcceptedConditionServes(t *testing.T) {
	for _, c := range []struct {
		name   string
		cond   api.Condition
		status int
	}{
		{"ResolvedRefs True is not Accepted", api.Condition{Type: gateway.CondResolvedRefs, Status: api.ConditionTrue}, http.StatusNotFound},
		{"control: Accepted True serves", api.Condition{Type: gateway.CondAccepted, Status: api.ConditionTrue}, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, proxy.Config{})
			route := customObject(t, gateway.KindHTTPRoute, "app",
				gateway.RouteSpec{ParentRefs: []gateway.ParentRef{{Name: gwName}}, Rules: []gateway.Rule{rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"})}},
				gateway.RouteStatus{Parents: []gateway.ParentStatus{{ParentRef: gateway.ParentRef{Name: gwName}, Conditions: []api.Condition{c.cond}}}})
			addr := serveHTTP(t, h, []api.CustomObject{route}, map[string][]string{"a": {serving(t, "a")}})
			status, _ := get(t, addr, "", "/")
			assert.Equal(t, c.status, status)
		})
	}
}

func TestHTTP_BackendIsTheEndpointsPortNamedLikeTheServicePort(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	o := objects(t, gatewayOf(t, httpListener("web", 80, "")),
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"}))},
		map[string][]string{"a": {serving(t, "web")}})
	host, port, err := net.SplitHostPort(serving(t, "metrics"))
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	o.Endpoints[0].Subsets = append(o.Endpoints[0].Subsets, api.EndpointSubset{
		Addresses: []api.EndpointAddress{{IP: host}},
		Ports:     []api.EndpointPort{{Name: "metrics", Port: int32(n), Protocol: api.ProtocolTCP}},
	})
	h.Apply(o)
	for range 6 {
		_, body := get(t, h.addr(t, 80), "", "/")
		require.Equal(t, "web", body, "only the Endpoints port named like the Service port is used")
	}
}

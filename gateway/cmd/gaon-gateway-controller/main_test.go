package main

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

func TestParseFlags(t *testing.T) {
	required := []string{"-server", "https://api.test/", "-proxy-image", "proxy:1", "-proxy-ca-configmap", "gaon-ca"}
	without := func(flag string) []string {
		var out []string
		for i := 0; i < len(required); i += 2 {
			if required[i] != flag {
				out = append(out, required[i], required[i+1])
			}
		}
		return out
	}
	t.Run("the required flags are enough, and the proxies default to the controller's server", func(t *testing.T) {
		cfg, err := parseFlags(required, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "https://api.test", cfg.server)
		assert.Equal(t, "https://api.test", cfg.gateway.Proxy.Server)
		assert.Equal(t, "proxy:1", cfg.gateway.Proxy.Image)
		assert.Equal(t, "gaon-ca", cfg.gateway.Proxy.CAConfigMap)
		assert.Equal(t, "gaon-gateway-proxy", cfg.gateway.Proxy.ServiceAccount)
		assert.Equal(t, 2*time.Second, cfg.gateway.Interval)
	})
	t.Run("the proxies take the server they are given", func(t *testing.T) {
		cfg, err := parseFlags(append([]string{"-proxy-server", "https://inside.test"}, required...), io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "https://inside.test", cfg.gateway.Proxy.Server)
	})
	for _, flag := range []string{"-server", "-proxy-image", "-proxy-ca-configmap"} {
		t.Run("a missing "+flag+" is refused", func(t *testing.T) {
			_, err := parseFlags(without(flag), io.Discard)
			assert.Error(t, err)
		})
	}
	t.Run("an interval of zero is refused", func(t *testing.T) {
		_, err := parseFlags(append([]string{"-interval", "0s"}, required...), io.Discard)
		assert.Error(t, err)
	})
}

// electionAPI is an API server that holds one coordination Lease with the
// compare-and-swap of the real one and one Gateway. It counts the requests each
// instance made, told apart by the X-Instance header its HTTP client adds.
type electionAPI struct {
	URL string

	mu      sync.Mutex
	lease   *api.CoordinationLease
	version int
	// work counts the requests other than the Lease's, by instance.
	work map[string]int
	// services counts the Service writes, by instance.
	services map[string]int
	// polls counts the Lease reads, by instance.
	polls map[string]int
}

func newElectionAPI(t *testing.T) *electionAPI {
	t.Helper()
	a := &electionAPI{work: map[string]int{}, services: map[string]int{}, polls: map[string]int{}}
	ts := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(ts.Close)
	a.URL = ts.URL
	return a
}

func (a *electionAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	who := r.Header.Get("X-Instance")
	w.Header().Set("Content-Type", "application/json")
	fail := func(reason api.Reason) {
		e := api.NewAPIError(reason, "object")
		w.WriteHeader(e.Code)
		_ = json.NewEncoder(w).Encode(e)
	}
	path := r.URL.Path
	if strings.Contains(path, "/"+api.CoordinationGroupVersion+"/leases/") {
		a.serveLease(w, r, who, fail)
		return
	}
	a.work[who]++
	switch {
	case strings.Contains(path, "/customresourcedefinitions/") && r.Method == http.MethodGet:
		fail(api.ReasonNotFound)
	case strings.HasSuffix(path, "/gateways"):
		gw := api.CustomObject{
			TypeMeta: api.TypeMeta{APIVersion: gateway.APIVersion, Kind: gateway.KindGateway},
			Metadata: api.ObjectMeta{Name: "edge", UID: "uid-edge", ResourceVersion: "1"},
			Spec:     json.RawMessage(`{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`),
		}
		_ = json.NewEncoder(w).Encode(api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{gw}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "s") && !strings.Contains(path, "/services/"):
		_ = json.NewEncoder(w).Encode(api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}})
	case r.Method == http.MethodPut && strings.Contains(path, "/services/"):
		a.services[who]++
		fallthrough
	default:
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}
}

func (a *electionAPI) serveLease(w http.ResponseWriter, r *http.Request, who string, fail func(api.Reason)) {
	switch r.Method {
	case http.MethodGet:
		a.polls[who]++
		if a.lease == nil {
			fail(api.ReasonNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(a.lease)
	case http.MethodPut:
		var l api.CoordinationLease
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			fail(api.ReasonInvalid)
			return
		}
		switch {
		case a.lease == nil && l.Metadata.ResourceVersion != "":
			fail(api.ReasonNotFound)
			return
		case a.lease != nil && l.Metadata.ResourceVersion == "":
			fail(api.ReasonAlreadyExists)
			return
		case a.lease != nil && l.Metadata.ResourceVersion != a.lease.Metadata.ResourceVersion:
			fail(api.ReasonConflict)
			return
		}
		a.version++
		l.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint(a.version))
		a.lease = &l
		_ = json.NewEncoder(w).Encode(l)
	}
}

func (a *electionAPI) count(m map[string]int, who string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return m[who]
}

func (a *electionAPI) workOf(who string) int     { return a.count(a.work, who) }
func (a *electionAPI) servicesOf(who string) int { return a.count(a.services, who) }
func (a *electionAPI) pollsOf(who string) int    { return a.count(a.polls, who) }

func (a *electionAPI) holder() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lease == nil {
		return ""
	}
	return a.lease.Spec.Holder
}

// tagged adds the X-Instance header to every request of a client.
type tagged struct {
	who  string
	next http.RoundTripper
}

func (t tagged) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Instance", t.who)
	return t.next.RoundTrip(r)
}

// startInstance runs the unit as main does, as the instance who, with short
// election times so that a test does not wait seconds.
func startInstance(t *testing.T, a *electionAPI, who string, extra ...string) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	args := append([]string{
		"-server", a.URL, "-interval", "50ms", "-proxy-image", "proxy:1", "-proxy-ca-configmap", "gaon-ca",
		"-leader-election-identity", who, "-leader-election-duration", "1s",
		"-leader-election-renew-deadline", "600ms", "-leader-election-retry-period", "100ms",
	}, extra...)
	cfg, err := parseFlags(args, io.Discard)
	require.NoError(t, err)
	cfg.httpClient.Transport = tagged{who: who, next: http.DefaultTransport}

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		defer close(out)
		out <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		<-out
	})
	return cancel, out
}

// The elector is proved with an injected clock in lib/api. These tests run the
// shipped wiring on the real clock, so they wait, with a deadline, for the state
// each step depends on: that an instance acted, or that the other one polled.
func TestRun_OnlyTheHolderOfTheLeaseWritesAndTheOtherTakesOverWhenItStops(t *testing.T) {
	a := newElectionAPI(t)

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return a.servicesOf("a") > 0 }, 5*time.Second, 10*time.Millisecond, "the first instance creates the Gateway's Service")
	require.Equal(t, "a", a.holder())

	startInstance(t, a, "b")
	require.Eventually(t, func() bool { return a.pollsOf("b") >= 5 }, 5*time.Second, 10*time.Millisecond, "the second instance polls the Lease")
	assert.Zero(t, a.workOf("b"), "the second instance polled the Lease and did nothing else")
	assert.Zero(t, a.servicesOf("b"))

	before := a.workOf("a")
	require.Eventually(t, func() bool { return a.workOf("a") > before }, 5*time.Second, 10*time.Millisecond, "the holder keeps sweeping")
	assert.Zero(t, a.workOf("b"), "control: the holder kept the Lease while it renewed")

	stopA()
	require.NoError(t, <-doneA)
	require.Eventually(t, func() bool { return a.servicesOf("b") > 0 }, 5*time.Second, 10*time.Millisecond, "the second instance takes over and acts")
	assert.Equal(t, "b", a.holder())
}

func TestRun_WithLeaderElectionOffActsAtOnceAndTouchesNoLease(t *testing.T) {
	a := newElectionAPI(t)
	startInstance(t, a, "solo", "-leader-elect=false")
	require.Eventually(t, func() bool { return a.servicesOf("solo") > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Zero(t, a.pollsOf("solo"))
	assert.Empty(t, a.holder())
}

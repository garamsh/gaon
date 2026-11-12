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

	"github.com/garamsh/gaon/lib/api"
)

// electionAPI is an API server that holds one coordination Lease with the
// compare-and-swap of the real one, and answers every other request with an
// empty list. It counts the requests each instance made, told apart by the
// X-Instance header its HTTP client adds.
type electionAPI struct {
	URL string

	mu      sync.Mutex
	lease   *api.CoordinationLease
	version int
	// work counts the requests other than the Lease's, by instance.
	work map[string]int
	// polls counts the Lease reads, by instance.
	polls map[string]int
}

func newElectionAPI(t *testing.T) *electionAPI {
	t.Helper()
	a := &electionAPI{work: map[string]int{}, polls: map[string]int{}}
	ts := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(ts.Close)
	a.URL = ts.URL
	return a
}

func (a *electionAPI) serve(w http.ResponseWriter, r *http.Request) {
	who := r.Header.Get("X-Instance")
	if r.URL.Query().Get("watch") == "true" {
		a.mu.Lock()
		a.work[who]++
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !strings.Contains(r.URL.Path, "/"+api.CoordinationGroupVersion+"/leases/") {
		a.work[who]++
		_, _ = io.WriteString(w, `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"NodeList","metadata":{"resourceVersion":"1"},"items":[]}`)
		return
	}
	fail := func(reason api.Reason) {
		e := api.NewAPIError(reason, "lease")
		w.WriteHeader(e.Code)
		_ = json.NewEncoder(w).Encode(e)
	}
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

func (a *electionAPI) workOf(who string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.work[who]
}

func (a *electionAPI) pollsOf(who string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.polls[who]
}

// writes counts the writes of the Lease: its creation, each renewal and each release.
func (a *electionAPI) writes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.version
}

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

// startInstance runs the controllers as main does, as the instance who, with
// short election times so that a test does not wait seconds.
func startInstance(t *testing.T, a *electionAPI, who string, extra ...string) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	args := append([]string{
		"-api", a.URL + "/apis/other.example/v1",
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
func TestRun_OnlyTheHolderOfTheLeaseRunsControllersAndTheOtherTakesOverWhenItStops(t *testing.T) {
	a := newElectionAPI(t)

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return a.workOf("a") > 0 }, 5*time.Second, 10*time.Millisecond, "the first instance runs its controllers")
	require.Equal(t, "a", a.holder())

	startInstance(t, a, "b")
	require.Eventually(t, func() bool { return a.pollsOf("b") >= 5 }, 5*time.Second, 10*time.Millisecond, "the second instance polls the Lease")
	assert.Zero(t, a.workOf("b"), "the second instance polled the Lease and ran no controller")

	before := a.writes()
	require.Eventually(t, func() bool { return a.writes() >= before+5 }, 5*time.Second, 10*time.Millisecond, "the holder renews the Lease")
	assert.Zero(t, a.workOf("b"), "control: the holder kept the Lease while it renewed")
	assert.Equal(t, "a", a.holder())

	stopA()
	require.NoError(t, <-doneA)
	require.Eventually(t, func() bool { return a.workOf("b") > 0 }, 5*time.Second, 10*time.Millisecond, "the second instance takes over")
	assert.Equal(t, "b", a.holder())
}

func TestRun_WithLeaderElectionOffRunsAtOnceAndTouchesNoLease(t *testing.T) {
	a := newElectionAPI(t)
	startInstance(t, a, "solo", "-leader-elect=false")
	require.Eventually(t, func() bool { return a.workOf("solo") > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Zero(t, a.pollsOf("solo"))
	assert.Empty(t, a.holder())
}

func TestRun_RefusesAnAPIWithoutAnAPIsPathWhenElecting(t *testing.T) {
	cfg, err := parseFlags([]string{"-api", "http://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)
	assert.ErrorContains(t, run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))), "/apis/")

	cfg, err = parseFlags([]string{"-api", "http://127.0.0.1:1", "-leader-elect=false"}, io.Discard)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))), "control: with election off the root needs no path")
}

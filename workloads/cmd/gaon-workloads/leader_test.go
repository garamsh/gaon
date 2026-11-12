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

const jobSpec = `{"completions":1,"backoffLimit":0,"template":{"image":"alpine","resources":{}}}`

// electionAPI is an API server that holds one coordination Lease with the
// compare-and-swap of the real one, one Job, and the MicroVMs written to it. It
// counts the requests each instance made, told apart by the X-Instance header
// its HTTP client adds.
type electionAPI struct {
	URL string

	mu       sync.Mutex
	lease    *api.CoordinationLease
	version  int
	microvms map[string]api.MicroVM
	// work counts the requests other than the Lease's, by instance.
	work map[string]int
	// creates counts the MicroVM writes, by instance.
	creates map[string]int
	// polls counts the Lease reads, by instance.
	polls map[string]int
}

func newElectionAPI(t *testing.T) *electionAPI {
	t.Helper()
	a := &electionAPI{microvms: map[string]api.MicroVM{}, work: map[string]int{}, creates: map[string]int{}, polls: map[string]int{}}
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
	switch {
	case strings.Contains(path, "/"+api.CoordinationGroupVersion+"/leases/"):
		a.serveLease(w, r, who, fail)
		return
	case strings.Contains(path, "/customresourcedefinitions/"):
		a.work[who]++
		if r.Method == http.MethodGet {
			fail(api.ReasonNotFound)
			return
		}
		echo(w, r)
	case strings.HasSuffix(path, "/jobs"):
		a.work[who]++
		_, _ = io.WriteString(w, `{"apiVersion":"workloads.gaon.garam.sh/v1alpha1","kind":"JobList","metadata":{"resourceVersion":"1"},"items":[{"apiVersion":"workloads.gaon.garam.sh/v1alpha1","kind":"Job","metadata":{"name":"build","uid":"uid-build","resourceVersion":"1"},"spec":`+jobSpec+`}]}`)
	case strings.HasSuffix(path, "/jobs/build/status"):
		a.work[who]++
		echo(w, r)
	case strings.HasSuffix(path, "/microvms") && r.Method == http.MethodGet:
		a.work[who]++
		list := api.MicroVMList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.MicroVM{}}
		for _, m := range a.microvms {
			list.Items = append(list.Items, m)
		}
		_ = json.NewEncoder(w).Encode(list)
	case strings.Contains(path, "/microvms/") && r.Method == http.MethodPut:
		a.work[who]++
		a.creates[who]++
		var m api.MicroVM
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			fail(api.ReasonInvalid)
			return
		}
		if _, ok := a.microvms[m.Metadata.Name]; ok {
			fail(api.ReasonAlreadyExists)
			return
		}
		a.microvms[m.Metadata.Name] = m
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(m)
	default:
		a.work[who]++
		fail(api.ReasonNotFound)
	}
}

// echo answers with the body it was sent, read whole first: the server cannot
// answer a request whose body it is still reading.
func echo(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_, _ = w.Write(body)
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

func (a *electionAPI) workOf(who string) int    { return a.count(a.work, who) }
func (a *electionAPI) createsOf(who string) int { return a.count(a.creates, who) }
func (a *electionAPI) pollsOf(who string) int   { return a.count(a.polls, who) }

func (a *electionAPI) holder() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lease == nil {
		return ""
	}
	return a.lease.Spec.Holder
}

func (a *electionAPI) microvmCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.microvms)
}

// dropMicroVMs removes every stored MicroVM, so that the Job needs one again.
func (a *electionAPI) dropMicroVMs() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.microvms = map[string]api.MicroVM{}
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
		"-server", a.URL, "-interval", "50ms",
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
func TestRun_OnlyTheHolderOfTheLeaseCreatesAJobsMicroVMsAndTheOtherTakesOverWhenItStops(t *testing.T) {
	a := newElectionAPI(t)

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return a.createsOf("a") > 0 }, 5*time.Second, 10*time.Millisecond, "the first instance creates the Job's MicroVM")
	require.Equal(t, "a", a.holder())

	startInstance(t, a, "b")
	require.Eventually(t, func() bool { return a.pollsOf("b") >= 5 }, 5*time.Second, 10*time.Millisecond, "the second instance polls the Lease")
	assert.Zero(t, a.workOf("b"), "the second instance polled the Lease and did nothing else")
	assert.Zero(t, a.createsOf("b"))

	before := a.workOf("a")
	require.Eventually(t, func() bool { return a.workOf("a") > before }, 5*time.Second, 10*time.Millisecond, "the holder keeps sweeping")
	assert.Zero(t, a.workOf("b"), "control: the holder kept the Lease while it renewed")
	assert.Equal(t, 1, a.microvmCount(), "exactly one MicroVM was created for the Job")

	stopA()
	require.NoError(t, <-doneA)
	a.dropMicroVMs()
	require.Eventually(t, func() bool { return a.createsOf("b") > 0 }, 5*time.Second, 10*time.Millisecond, "the second instance takes over and creates the Job's MicroVM")
	assert.Equal(t, "b", a.holder())
}

func TestRun_WithLeaderElectionOffCreatesAtOnceAndTouchesNoLease(t *testing.T) {
	a := newElectionAPI(t)
	startInstance(t, a, "solo", "-leader-elect=false")
	require.Eventually(t, func() bool { return a.createsOf("solo") > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Zero(t, a.pollsOf("solo"))
	assert.Empty(t, a.holder())
}

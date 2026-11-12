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
	"github.com/garamsh/gaon/workloads/internal/job"
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
	// cron is the CronJob the server holds, if a test sets one.
	cron *api.CustomObject
	// jobs are the Jobs written to the server, and jobPuts counts the writes of
	// them, by instance, whether or not they were refused.
	jobs    map[string]api.CustomObject
	jobPuts map[string]int
	// clock, when set, is the time the instances read.
	clock job.Clock
	// deletes counts the MicroVM deletions, by instance.
	deletes map[string]int
	// dep is the Deployment the server holds, if a test sets one, and the state
	// of the replicas it checks and the agents it plays.
	dep   *api.CustomObject
	fleet fleetState
}

func newElectionAPI(t *testing.T) *electionAPI {
	t.Helper()
	a := &electionAPI{microvms: map[string]api.MicroVM{}, work: map[string]int{}, creates: map[string]int{}, polls: map[string]int{},
		jobs: map[string]api.CustomObject{}, jobPuts: map[string]int{}, deletes: map[string]int{}}
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
		items := []api.CustomObject{{
			TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.Kind},
			Metadata: api.ObjectMeta{Name: "build", UID: "uid-build", ResourceVersion: "1"},
			Spec:     json.RawMessage(jobSpec),
		}}
		for _, j := range a.jobs {
			items = append(items, j)
		}
		_ = json.NewEncoder(w).Encode(api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: "JobList"}, Metadata: api.ListMeta{ResourceVersion: "1"}, Items: items})
	case strings.Contains(path, "/jobs/") && strings.HasSuffix(path, "/status"):
		a.work[who]++
		echo(w, r)
	case strings.Contains(path, "/jobs/") && r.Method == http.MethodPut:
		a.work[who]++
		a.jobPuts[who]++
		var j api.CustomObject
		if err := json.NewDecoder(r.Body).Decode(&j); err != nil {
			fail(api.ReasonInvalid)
			return
		}
		if _, ok := a.jobs[j.Metadata.Name]; ok {
			fail(api.ReasonAlreadyExists)
			return
		}
		j.Metadata.UID, j.Metadata.ResourceVersion = api.NewUID(), "1"
		// The Job has run to completion, so the next time is not skipped for it.
		j.Status = json.RawMessage(`{"succeeded":1,"conditions":[{"type":"Complete","status":"True"}]}`)
		a.jobs[j.Metadata.Name] = j
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(j)
	case strings.HasSuffix(path, "/cronjobs"):
		a.work[who]++
		list := api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: "CronJobList"}, Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
		if a.cron != nil {
			list.Items = append(list.Items, *a.cron)
		}
		_ = json.NewEncoder(w).Encode(list)
	case strings.Contains(path, "/cronjobs/") && strings.HasSuffix(path, "/status"):
		a.work[who]++
		var in api.CustomObject
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || a.cron == nil {
			fail(api.ReasonInvalid)
			return
		}
		if in.Metadata.ResourceVersion != a.cron.Metadata.ResourceVersion {
			fail(api.ReasonConflict)
			return
		}
		a.version++
		a.cron.Status = in.Status
		a.cron.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("c", a.version))
		_ = json.NewEncoder(w).Encode(a.cron)
	case strings.HasSuffix(path, "/deployments"):
		a.work[who]++
		a.listDeployments(w)
	case strings.Contains(path, "/deployments/") && strings.HasSuffix(path, "/status"):
		a.work[who]++
		a.putDeploymentStatus(w, r, fail)
	case strings.Contains(path, "/microvms/") && r.Method == http.MethodDelete:
		a.work[who]++
		a.deletes[who]++
		a.deleteMicroVM(w, r, fail)
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
		a.version++
		m.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("m", a.version))
		a.microvms[m.Metadata.Name] = m
		a.checkBounds()
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
func (a *electionAPI) jobPutsOf(who string) int { return a.count(a.jobPuts, who) }

// scheduledJobs returns the number of Jobs a CronJob created.
func (a *electionAPI) scheduledJobs() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.jobs)
}
func (a *electionAPI) pollsOf(who string) int { return a.count(a.polls, who) }

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
	if a.clock != nil {
		cfg.clock = a.clock
	}

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

// settableClock reads a time a test moves and waits on the real clock, so the
// shipped wiring sweeps as it does while the schedule's time is the test's.
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *settableClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (c *settableClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func TestRun_TwoInstancesCreateOneJobPerScheduledTimeAndTheOtherTakesOver(t *testing.T) {
	a := newElectionAPI(t)
	at := func(s string) time.Time {
		ts, err := time.Parse("2006-01-02 15:04:05", s)
		require.NoError(t, err)
		return ts
	}
	clock := &settableClock{now: at("2026-11-13 12:01:10")}
	a.clock = clock
	a.cron = &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.KindCronJob},
		Metadata: api.ObjectMeta{Name: "snap", UID: "uid-snap", ResourceVersion: "c0"},
		Spec:     json.RawMessage(`{"schedule":"* * * * *","jobTemplate":` + jobSpec + `}`),
		Status:   json.RawMessage(`{"lastScheduleTime":"2026-11-13T12:00:00Z"}`),
	}

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return a.scheduledJobs() == 1 }, 5*time.Second, 10*time.Millisecond, "the first instance creates the Job of 12:01")

	startInstance(t, a, "b")
	require.Eventually(t, func() bool { return a.pollsOf("b") >= 5 }, 5*time.Second, 10*time.Millisecond, "the second instance polls the Lease")
	assert.Zero(t, a.jobPutsOf("b"), "the instance without the Lease creates no Job")

	clock.set(at("2026-11-13 12:02:10"))
	require.Eventually(t, func() bool { return a.scheduledJobs() == 2 }, 5*time.Second, 10*time.Millisecond, "the holder creates the Job of 12:02")
	assert.Zero(t, a.jobPutsOf("b"))

	stopA()
	require.NoError(t, <-doneA)
	clock.set(at("2026-11-13 12:03:10"))
	require.Eventually(t, func() bool { return a.scheduledJobs() == 3 }, 5*time.Second, 10*time.Millisecond, "the second instance takes over and creates the Job of 12:03")
	assert.Equal(t, 3, a.jobPutsOf("a")+a.jobPutsOf("b"), "every Job was written once: no instance wrote a Job for a time already run")
	assert.Equal(t, 1, a.jobPutsOf("b"))
}

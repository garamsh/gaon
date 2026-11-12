package api_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

const (
	electionDuration = 15 * time.Second
	electionDeadline = 10 * time.Second
	electionRetry    = 2 * time.Second
)

// electionClock is a Clock a test advances. Each pending After is kept with the
// duration it was asked for, so a test can tell the timers of the electors,
// which ask for the retry period, from a renewal deadline that never fires.
type electionClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []electionTimer
}

type electionTimer struct {
	at   time.Time
	d    time.Duration
	fire chan time.Time
}

func newElectionClock() *electionClock {
	return &electionClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *electionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *electionClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.pending = append(c.pending, electionTimer{at: c.now.Add(d), d: d, fire: ch})
	return ch
}

// advance moves the clock by d and fires the timers that are due.
func (c *electionClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.pending[:0]
	for _, t := range c.pending {
		if t.at.After(c.now) {
			kept = append(kept, t)
			continue
		}
		t.fire <- c.now
	}
	c.pending = kept
}

// retryTimers counts the pending timers of the retry period.
func (c *electionClock) retryTimers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.pending {
		if t.d == electionRetry {
			n++
		}
	}
	return n
}

// leaseStore is the coordination Lease of an API server: a write succeeds only
// at the version the Lease has.
type leaseStore struct {
	mu      sync.Mutex
	lease   *api.CoordinationLease
	version int
	// acquired counts, by holder, the writes that made it the holder.
	acquired map[string]int
}

func (s *leaseStore) acquisitions(holder string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquired[holder]
}

func (s *leaseStore) get() (*api.CoordinationLease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lease == nil {
		return nil, false
	}
	l := *s.lease
	return &l, true
}

func (s *leaseStore) put(l *api.CoordinationLease) (*api.CoordinationLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.lease == nil && l.Metadata.ResourceVersion != "":
		return nil, api.NewAPIError(api.ReasonNotFound, "no lease")
	case s.lease != nil && l.Metadata.ResourceVersion == "":
		return nil, api.NewAPIError(api.ReasonAlreadyExists, "lease exists")
	case s.lease != nil && l.Metadata.ResourceVersion != s.lease.Metadata.ResourceVersion:
		return nil, api.NewAPIError(api.ReasonConflict, "stale lease")
	}
	if l.Spec.Holder != "" && (s.lease == nil || s.lease.Spec.Holder != l.Spec.Holder) {
		if s.acquired == nil {
			s.acquired = map[string]int{}
		}
		s.acquired[l.Spec.Holder]++
	}
	s.version++
	stored := *l
	stored.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint(s.version))
	s.lease = &stored
	out := stored
	return &out, nil
}

// memberClient is one elector's connection to the store. A cut connection
// fails every call, or holds it until the caller gives up.
type memberClient struct {
	store *leaseStore
	mu    sync.Mutex
	cut   string
	// held counts the calls that were held because the connection hangs.
	held atomic.Int32
	// skew is added to the renew time of every Lease a write returns, as a server
	// whose clock is not the elector's would stamp it.
	skew time.Duration
}

// Ways to cut a connection.
const (
	connected = ""
	failing   = "fail"
	hanging   = "hang"
)

func (m *memberClient) setCut(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cut = mode
}

func (m *memberClient) isHanging() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cut == hanging
}

func (m *memberClient) gate(ctx context.Context) error {
	m.mu.Lock()
	mode := m.cut
	m.mu.Unlock()
	switch mode {
	case failing:
		return fmt.Errorf("connection refused")
	case hanging:
		m.held.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (m *memberClient) GetCoordinationLease(ctx context.Context, _ string) (*api.CoordinationLease, error) {
	if err := m.gate(ctx); err != nil {
		return nil, err
	}
	l, ok := m.store.get()
	if !ok {
		return nil, api.NewAPIError(api.ReasonNotFound, "no lease")
	}
	return l, nil
}

func (m *memberClient) PutCoordinationLease(ctx context.Context, l *api.CoordinationLease) (*api.CoordinationLease, error) {
	if err := m.gate(ctx); err != nil {
		return nil, err
	}
	stored, err := m.store.put(l)
	if err == nil {
		stored.Spec.RenewTime = stored.Spec.RenewTime.Add(m.skew)
	}
	return stored, err
}

// member is one process that contends for the Lease and records when it acts.
type member struct {
	id     string
	client *memberClient
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	acting bool
	terms  int
}

func (m *member) isActing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acting
}

func (m *member) termCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terms
}

type election struct {
	t       *testing.T
	clock   *electionClock
	store   *leaseStore
	members []*member
	// skew is given to the client of every member started.
	skew time.Duration
}

func newElection(t *testing.T, ids ...string) *election {
	t.Helper()
	e := &election{t: t, clock: newElectionClock(), store: &leaseStore{}}
	for _, id := range ids {
		e.members = append(e.members, e.start(id))
	}
	e.settle()
	return e
}

func (e *election) start(id string) *member {
	m := &member{id: id, client: &memberClient{store: e.store, skew: e.skew}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	elector := &api.LeaderElector{
		Leases: m.client, Name: "scheduler", Identity: id, Clock: e.clock,
		Duration: electionDuration, RenewDeadline: electionDeadline, RetryPeriod: electionRetry,
	}
	go func() {
		defer close(m.done)
		_ = elector.Run(ctx, func(ctx context.Context) {
			m.mu.Lock()
			m.acting = true
			m.terms++
			m.mu.Unlock()
			<-ctx.Done()
			m.mu.Lock()
			m.acting = false
			m.mu.Unlock()
		})
	}()
	e.t.Cleanup(func() {
		cancel()
		<-m.done
	})
	return m
}

// settle waits until every elector is waiting for its next retry, so that the
// next advance of the clock reaches all of them. The wait is on the condition
// the next step depends on: every running elector has armed its retry timer.
func (e *election) settle() {
	e.t.Helper()
	waiting, hung := 0, 0
	for _, m := range e.members {
		select {
		case <-m.done:
			continue
		default:
		}
		if m.client.isHanging() {
			hung++
		} else {
			waiting++
		}
	}
	require.Eventually(e.t, func() bool {
		return e.clock.retryTimers() == waiting && e.heldCalls() >= hung && e.leadersStarted()
	}, 5*time.Second, time.Millisecond, "electors did not all wait for their next retry or hold a call")
}

// leadersStarted reports whether every term a member took the Lease for has
// called its lead function, which Run does on a goroutine of its own.
func (e *election) leadersStarted() bool {
	for _, m := range e.members {
		if m.termCount() != e.store.acquisitions(m.id) {
			return false
		}
	}
	return true
}

// heldCalls counts the calls held on the hanging connections, which is also
// the proof that the renewal deadline of the caller is armed.
func (e *election) heldCalls() int {
	n := 0
	for _, m := range e.members {
		if m.client.isHanging() && m.client.held.Load() > 0 {
			n++
		}
	}
	return n
}

// step advances the clock by one retry period and lets every elector act on it.
// It returns the ids that act afterwards.
func (e *election) step() []string {
	e.t.Helper()
	e.clock.advance(electionRetry)
	e.settle()
	return e.acting()
}

func (e *election) acting() []string {
	var ids []string
	for _, m := range e.members {
		if m.isActing() {
			ids = append(ids, m.id)
		}
	}
	sort.Strings(ids)
	return ids
}

// runFor steps for d and fails if more than one member ever acts at once. It
// returns the ids that acted at the end.
func (e *election) runFor(d time.Duration) []string {
	e.t.Helper()
	var last []string
	for elapsed := time.Duration(0); elapsed < d; elapsed += electionRetry {
		last = e.step()
		require.LessOrEqual(e.t, len(last), 1, "two members act at %s", e.clock.Now())
	}
	return last
}

func (e *election) member(id string) *member {
	for _, m := range e.members {
		if m.id == id {
			return m
		}
	}
	e.t.Fatalf("no member %s", id)
	return nil
}

func TestLeaderElector_ExactlyOneCandidateActs(t *testing.T) {
	for _, ids := range [][]string{{"a", "b"}, {"a", "b", "c"}} {
		t.Run(fmt.Sprintf("%d candidates", len(ids)), func(t *testing.T) {
			e := newElection(t, ids...)
			acting := e.runFor(3 * electionDuration)
			require.Len(t, acting, 1, "one member acts")

			total := 0
			for _, m := range e.members {
				total += m.termCount()
			}
			assert.Equal(t, 1, total, "only one term was ever led: the other candidates never acted")
		})
	}
}

func TestLeaderElector_LeaderThatRenewsKeepsLeadingAndNoOtherTakesOver(t *testing.T) {
	e := newElection(t, "a", "b", "c")
	first := e.runFor(2 * electionDuration)
	require.Len(t, first, 1)
	leader := e.member(first[0])
	require.Equal(t, 1, leader.termCount())

	// The control of the takeover tests: the same run, with the leader healthy,
	// ends with the same leader after many Durations.
	for range 10 {
		assert.Equal(t, first, e.runFor(electionDuration))
	}
	assert.Equal(t, 1, leader.termCount(), "the leader never lost the Lease")
	l, ok := e.store.get()
	require.True(t, ok)
	assert.Equal(t, leader.id, l.Spec.Holder)
}

func TestLeaderElector_AnotherTakesOverWithinTheDurationAfterTheLeaderStops(t *testing.T) {
	for _, mode := range []string{failing, hanging} {
		t.Run("connection "+mode, func(t *testing.T) {
			e := newElection(t, "a", "b", "c")
			first := e.runFor(2 * electionDuration)
			require.Len(t, first, 1)
			leader := e.member(first[0])
			lease, _ := e.store.get()
			require.Equal(t, leader.id, lease.Spec.Holder)

			// The leader can no longer reach the API server. Its last
			// acknowledged renewal is the one the store holds.
			leader.client.setCut(mode)
			const bound = electionDuration + 2*electionRetry // one poll to see the last renewal, one to act
			var tookOver time.Duration
			for elapsed := electionRetry; elapsed <= bound; elapsed += electionRetry {
				acting := e.step()
				require.LessOrEqual(t, len(acting), 1, "two members act at %s", e.clock.Now())
				switch {
				case elapsed < electionDeadline:
					assert.True(t, leader.isActing(), "the leader acts until its renew deadline, at %s", elapsed)
				case elapsed == electionDeadline:
					// The step that stops the leader ends on the elector's own goroutine, so it is waited on.
					require.Eventually(t, func() bool { return !leader.isActing() }, 5*time.Second, time.Millisecond,
						"the leader still acts at the renew deadline")
				}
				if elapsed < electionDuration {
					assert.Empty(t, others(acting, leader.id), "no one takes over before the Lease lapsed, at %s", elapsed)
				}
				if tookOver == 0 && len(others(acting, leader.id)) == 1 {
					tookOver = elapsed
				}
			}
			if tookOver == 0 {
				require.Eventually(t, func() bool { return len(others(e.acting(), leader.id)) == 1 }, 5*time.Second, time.Millisecond,
					"no other member acts %s after the last renewal", bound)
				tookOver = bound
			}
			assert.False(t, leader.isActing(), "the old leader stays stopped")
			assert.Greater(t, tookOver, electionDeadline, "the new leader starts after the old one stopped")
			assert.GreaterOrEqual(t, tookOver, electionDuration, "no one takes over before the Lease lapsed")
			assert.LessOrEqual(t, tookOver, bound)
		})
	}
}

// others returns the ids in acting other than id.
func others(acting []string, id string) []string {
	var out []string
	for _, a := range acting {
		if a != id {
			out = append(out, a)
		}
	}
	return out
}

func TestLeaderElector_LeaderStopsActingWhenAnotherWroteTheLease(t *testing.T) {
	e := newElection(t, "a", "b")
	first := e.runFor(electionDuration)
	require.Len(t, first, 1)
	leader := e.member(first[0])

	// Someone else holds the Lease now, at a newer version than the leader's.
	cur, _ := e.store.get()
	taken := *cur
	taken.Spec.Holder = "someone-else"
	_, err := e.store.put(&taken)
	require.NoError(t, err)

	e.step()
	assert.False(t, leader.isActing(), "the next renewal conflicts, so the leader stops at once")
	assert.Empty(t, e.acting(), "the Lease is held and live, so no candidate acts")
}

func TestLeaderElector_ReleaseLetsAnotherTakeOverAtOnce(t *testing.T) {
	e := newElection(t, "a", "b")
	first := e.runFor(electionDuration)
	require.Len(t, first, 1)
	leader := e.member(first[0])

	leader.cancel()
	<-leader.done
	require.False(t, leader.isActing())
	lease, _ := e.store.get()
	require.Empty(t, lease.Spec.Holder, "the Lease was released")

	acting := e.step()
	require.Len(t, acting, 1, "the other member acts one retry period after the release, far inside the duration")
	assert.NotEqual(t, leader.id, acting[0])
}

func TestLeaderElector_ACandidateWaitsTheFullDurationFromItsFirstSight(t *testing.T) {
	store := &leaseStore{}
	_, err := store.put(&api.CoordinationLease{
		TypeMeta: api.TypeMeta{APIVersion: api.CoordinationGroupVersion, Kind: api.KindCoordinationLease},
		Metadata: api.ObjectMeta{Name: "scheduler"},
		Spec:     api.CoordinationLeaseSpec{Holder: "dead", RenewTime: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), DurationSeconds: 15},
	})
	require.NoError(t, err)
	e := &election{t: t, clock: newElectionClock(), store: store}
	e.members = append(e.members, e.start("a"))
	e.settle()

	// The holder's renewTime is long past, but a candidate does not compare it
	// with its own clock: it waits the duration from the first version it saw.
	for elapsed := electionRetry; elapsed < electionDuration; elapsed += electionRetry {
		assert.Empty(t, e.step(), "at %s after the first sight", elapsed)
	}
	assert.Equal(t, []string{"a"}, e.step())
}

func TestLeaderElector_RunRefusesSettingsThatLetTwoHoldersAct(t *testing.T) {
	good := api.LeaderElector{
		Leases: &memberClient{store: &leaseStore{}}, Name: "scheduler", Identity: "a", Clock: newElectionClock(),
		Duration: electionDuration, RenewDeadline: electionDeadline, RetryPeriod: electionRetry,
	}
	require.NoError(t, good.Validate(), "the control: the settings the other tests run with")

	for name, change := range map[string]func(*api.LeaderElector){
		"renew deadline at the duration": func(e *api.LeaderElector) { e.RenewDeadline = e.Duration },
		"retry period at the deadline":   func(e *api.LeaderElector) { e.RetryPeriod = e.RenewDeadline },
		"duration not in whole seconds":  func(e *api.LeaderElector) { e.Duration = 15500 * time.Millisecond },
		"no identity":                    func(e *api.LeaderElector) { e.Identity = "" },
		"no clock":                       func(e *api.LeaderElector) { e.Clock = nil },
		"no retry period":                func(e *api.LeaderElector) { e.RetryPeriod = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			e := good
			change(&e)
			assert.Error(t, e.Validate())
			assert.Error(t, e.Run(context.Background(), func(context.Context) { t.Error("lead was called") }))
		})
	}
}

// The first term's renew deadline counts from the elector's own reading of its
// clock when the take began, not from the renew time the server returns, which
// is a wall-clock value another host may have stamped.
func TestLeaderElector_TheFirstTermEndsAtTheRenewDeadlineWhateverRenewTimeTheServerReturns(t *testing.T) {
	for name, skew := range map[string]time.Duration{"server agrees": 0, "server an hour ahead": time.Hour} {
		t.Run(name, func(t *testing.T) {
			e := &election{t: t, clock: newElectionClock(), store: &leaseStore{}, skew: skew}
			m := e.start("a")
			e.members = []*member{m}
			e.settle()
			require.True(t, m.isActing(), "the member took the Lease")
			took := e.clock.Now()

			// No renewal is acknowledged from here on.
			m.client.setCut(failing)
			for e.clock.Now().Add(electionRetry).Before(took.Add(electionDeadline)) {
				require.Equal(t, []string{"a"}, e.step(), "still acting at %s, before the renew deadline", e.clock.Now())
			}
			assert.Empty(t, e.step(), "stops acting at the take's start plus the renew deadline")
			assert.Equal(t, took.Add(electionDeadline), e.clock.Now())
		})
	}
}

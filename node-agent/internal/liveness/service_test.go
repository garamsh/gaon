package liveness_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/liveness"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// params has a δ large against the call timeout, so a check that leaves δ
// out lets a renewal through that a check with it refuses.
var params = liveness.Params{
	LeaseDuration:  30 * time.Second,
	RenewPeriod:    5 * time.Second,
	DeadlineLength: 15 * time.Second,
	Delta:          8 * time.Second,
	RPCTimeout:     time.Second,
}

// lateAck wraps mvmd's deadline calls the way a lost acknowledgement looks to
// the agent: the call takes the whole timeout to reach mvmd, which applies
// it, and the agent sees an error.
type lateAck struct {
	liveness.Deadline
	clock *clock.Fake
	delay time.Duration
	on    bool
}

func (l *lateAck) RenewDeadline(ctx context.Context, in *v1alpha2.RenewDeadlineRequest, opts ...grpc.CallOption) (*v1alpha2.RenewDeadlineResponse, error) {
	if !l.on {
		return l.Deadline.RenewDeadline(ctx, in, opts...)
	}
	l.clock.Advance(l.delay)
	_, _ = l.Deadline.RenewDeadline(ctx, in, opts...)
	return nil, context.DeadlineExceeded
}

type env struct {
	clock *clock.Fake
	gaon  *fakegaon.Server
	mvmd  *fakemvmd.Server
	deadl liveness.Deadline
	svc   func(liveness.Deadline) liveness.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	gaon, mvmd := fakegaon.New(t), fakemvmd.New(t, clk)
	conn, err := runtimeapi.Dial(context.Background(), mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := v1alpha2.NewRuntimeServiceClient(conn)
	leases := api.NewClient(gaon.URL, nil)
	return &env{clock: clk, gaon: gaon, mvmd: mvmd, deadl: client, svc: func(d liveness.Deadline) liveness.Service {
		return liveness.NewService("n1", params, leases, d, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}}
}

// lastValidity is the end of validity of the latest Lease write the API
// acknowledged, from the renew time the agent wrote.
func (e *env) lastValidity(t *testing.T) time.Time {
	t.Helper()
	writes := e.gaon.LeaseWrites()
	require.NotEmpty(t, writes)
	last := writes[len(writes)-1].Spec
	return last.RenewTime.Add(time.Duration(last.DurationSeconds) * time.Second)
}

// assertFenced checks the obligation: no recorded expiry, plus δ, is later
// than the validity of the latest acknowledged Lease renewal.
func (e *env) assertFenced(t *testing.T) {
	t.Helper()
	valid := e.lastValidity(t)
	for _, r := range e.mvmd.Renewals() {
		assert.False(t, r.Expiry.Add(params.Delta).After(valid),
			"renewal %d expires at %s, plus δ %s, after the Lease validity %s", r.Number, r.Expiry, params.Delta, valid)
	}
}

func TestRenew_DeadlineExpiryPlusDeltaStaysWithinLeaseValidity(t *testing.T) {
	e := newEnv(t)
	svc := e.svc(e.deadl)
	for range 20 {
		require.NoError(t, svc.Renew(context.Background()))
		e.assertFenced(t)
		e.clock.Advance(params.RenewPeriod)
	}
	require.Len(t, e.mvmd.Renewals(), 20, "setup: every round renewed the deadline, so the check ran against all of them")
}

func TestRenew_StopsRenewingDeadlineWhenLeaseCannotBeRenewed(t *testing.T) {
	e := newEnv(t)
	svc := e.svc(e.deadl)
	require.NoError(t, svc.Renew(context.Background()))
	e.gaon.FailLeaseWrites(true)
	var refused int
	for range 20 {
		e.clock.Advance(params.RenewPeriod)
		if err := svc.Renew(context.Background()); err != nil {
			refused++
		}
		e.assertFenced(t)
	}
	assert.Equal(t, 20, refused, "every round after the Lease writes fail reports the failure")
	renewals := e.mvmd.Renewals()
	assert.Less(t, len(renewals), 4, "the deadline is renewed only while the Lease leaves room, not for all 20 rounds")
	assert.GreaterOrEqual(t, len(renewals), 2, "control: the deadline was renewed while the Lease still left room")
}

func TestRenew_BoundsAnUnacknowledgedDeadlineRenewalByTheCallTimeout(t *testing.T) {
	e := newEnv(t)
	late := &lateAck{Deadline: e.deadl, clock: e.clock, delay: params.RPCTimeout}
	svc := e.svc(late)
	require.NoError(t, svc.Renew(context.Background()))
	e.gaon.FailLeaseWrites(true)
	// From now mvmd receives a renewal as late as the timeout allows and the
	// agent never sees the acknowledgement.
	late.on = true
	e.clock.Advance(6500 * time.Millisecond)
	err := svc.Renew(context.Background())
	require.Error(t, err)
	e.assertFenced(t)
	assert.Len(t, e.mvmd.Renewals(), 1, "no renewal reached mvmd after the first, which a round at 6.5s past the Lease renewal could only send unfenced")
}

func TestRenew_RenewalNumbersIncreaseAcrossRestarts(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.svc(e.deadl).Renew(context.Background()))
	require.NoError(t, e.svc(e.deadl).Renew(context.Background()), "a new service, as after a restart, reads the number from mvmd")
	renewals := e.mvmd.Renewals()
	require.Len(t, renewals, 2)
	assert.Greater(t, renewals[1].Number, renewals[0].Number)
}

func TestRenew_CreatesTheLeaseNamedAsTheNodeThenRenewsItByVersion(t *testing.T) {
	e := newEnv(t)
	svc := e.svc(e.deadl)
	require.NoError(t, svc.Renew(context.Background()))
	first, ok := e.gaon.Lease("n1")
	require.True(t, ok, "the Lease is named as the node")
	assert.Equal(t, "n1", first.Spec.Holder)
	assert.EqualValues(t, 30, first.Spec.DurationSeconds)

	e.clock.Advance(params.RenewPeriod)
	require.NoError(t, svc.Renew(context.Background()), "the second write carries the version of the first, or the API refuses it")
	second, _ := e.gaon.Lease("n1")
	assert.Greater(t, second.Spec.RenewTime, first.Spec.RenewTime)
	assert.Len(t, e.gaon.LeaseWrites(), 2)
}

func TestRun_KeepsTheObligationUntilTheContextEnds(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc(e.deadl).Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return len(e.mvmd.Renewals()) >= 5 }, 5*time.Second, 5*time.Millisecond)
	e.gaon.FailLeaseWrites(true)
	n := len(e.gaon.LeaseWrites())
	require.Eventually(t, func() bool { return e.clock.Now().After(e.lastValidity(t).Add(time.Minute)) }, 5*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	assert.Len(t, e.gaon.LeaseWrites(), n, "setup: no Lease write was acknowledged after the failure")
	e.assertFenced(t)
}

// hangInspect is a runtime that does not answer InspectDeadline: the call
// returns only when its context ends.
type hangInspect struct {
	liveness.Deadline
	on bool
}

func (h *hangInspect) InspectDeadline(ctx context.Context, in *v1alpha2.InspectDeadlineRequest, opts ...grpc.CallOption) (*v1alpha2.InspectDeadlineResponse, error) {
	if !h.on {
		return h.Deadline.InspectDeadline(ctx, in, opts...)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRenew_ARuntimeThatDoesNotAnswerInspectDoesNotHoldTheRoundAndTheNextRoundRenewsTheLease(t *testing.T) {
	e := newEnv(t)
	hang := &hangInspect{Deadline: e.deadl, on: true}
	fast := params
	fast.RPCTimeout = 50 * time.Millisecond // a real timeout: the fake clock does not move contexts
	svc := liveness.NewService("n1", fast, api.NewClient(e.gaon.URL, nil), hang, e.clock, slog.New(slog.NewTextHandler(io.Discard, nil)))

	returned := make(chan error, 1)
	go func() { returned <- svc.Renew(context.Background()) }()
	select {
	case err := <-returned:
		require.Error(t, err, "the round reports the runtime that did not answer")
	case <-time.After(5 * time.Second):
		t.Fatal("the round did not return while the runtime does not answer InspectDeadline")
	}
	assert.Len(t, e.gaon.LeaseWrites(), 1, "the Lease was renewed in the round that the runtime held")

	hang.on = false
	e.clock.Advance(params.RenewPeriod)
	require.NoError(t, svc.Renew(context.Background()), "control: the same service renews once the runtime answers")
	assert.Len(t, e.gaon.LeaseWrites(), 2, "the next round renews the Lease")
	assert.Len(t, e.mvmd.Renewals(), 1, "and the deadline")
	e.assertFenced(t)
}

func TestParamsValidate(t *testing.T) {
	ok := params
	tests := []struct {
		name   string
		mutate func(*liveness.Params)
		valid  bool
	}{
		{"as configured", func(*liveness.Params) {}, true},
		{"zero delta", func(p *liveness.Params) { p.Delta = 0 }, false},
		{"lease duration of fractions of a second", func(p *liveness.Params) { p.LeaseDuration = 30500 * time.Millisecond }, false},
		{"deadline not outlasting the gap between rounds", func(p *liveness.Params) { p.DeadlineLength = 6 * time.Second }, false},
		{"lease too short for the deadline and δ", func(p *liveness.Params) { p.LeaseDuration = 24 * time.Second }, false},
		{"lease exactly the deadline, δ and two timeouts", func(p *liveness.Params) { p.LeaseDuration = 25 * time.Second }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ok
			tt.mutate(&p)
			if tt.valid {
				assert.NoError(t, p.Validate())
			} else {
				assert.Error(t, p.Validate())
			}
		})
	}
}

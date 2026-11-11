package nodelifecycle_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/nodelifecycle"
	"github.com/garamsh/gaon/lib/api"
)

const (
	leaseSeconds = 10
	margin       = 5 * time.Second
	validity     = leaseSeconds*time.Second + margin
)

// start runs the controller against f on a fake clock until the test ends,
// and returns once it has finished its first pass over the listed Leases.
func start(t *testing.T, f *fakeAPI, clock *fakeClock) {
	t.Helper()
	svc := nodelifecycle.NewService(f.client, clock, nodelifecycle.Config{Margin: margin}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})
	clock.waitCallsAbove(t, 0)
}

// pass sends a watch event and waits until the controller has processed it
// and finished its pass over the Leases.
func pass(t *testing.T, f *fakeAPI, clock *fakeClock, typ api.EventType, l api.Lease) {
	t.Helper()
	before := clock.Calls()
	f.emit(typ, l)
	clock.waitCallsAbove(t, before)
}

// setup serves one Lease and its Node and starts the controller.
func setup(t *testing.T) (*fakeAPI, *fakeClock) {
	t.Helper()
	f := newFakeAPI(t)
	f.addNode(newNode("node-a"))
	f.setLeases("10", newLease("node-a", "1", leaseSeconds))
	clock := newFakeClock()
	start(t, f, clock)
	return f, clock
}

func TestLeaseExpiredIsSetOnlyOnceDurationPlusMarginHasElapsed(t *testing.T) {
	f, clock := setup(t)
	same := newLease("node-a", "1", leaseSeconds)

	clock.Advance(validity - time.Nanosecond)
	pass(t, f, clock, api.EventModified, same)
	assert.Zero(t, f.putCount(), "the Node was written before the validity passed")
	f.notExpired("node-a")

	clock.Advance(time.Nanosecond)
	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionTrue)
	c, _ := f.leaseExpired("node-a")
	assert.Equal(t, "LeaseExpired", c.Reason)
	require.NotNil(t, c.LastTransitionTime)
}

func TestLeaseExpiredKeepsTheNodeAgentsConditions(t *testing.T) {
	f, clock := setup(t)

	clock.Advance(validity)
	f.waitPuts(1)

	f.mu.Lock()
	defer f.mu.Unlock()
	got := f.nodes["node-a"]
	require.Len(t, got.Status.Conditions, 2)
	assert.Equal(t, api.NodeReady, got.Status.Conditions[0].Type)
	assert.Equal(t, api.ConditionTrue, got.Status.Conditions[0].Status)
	assert.Equal(t, api.NodeStateAdmitted, got.Status.State)
	assert.True(t, got.Spec.Admitted)
}

func TestValidityRunsFromTheFirstSightingOfAVersionNotFromTheRenewTime(t *testing.T) {
	f, clock := setup(t)

	// A renewal seen 8s in: the old version's deadline (15s) no longer holds.
	clock.Advance(8 * time.Second)
	pass(t, f, clock, api.EventModified, newLease("node-a", "2", leaseSeconds))

	clock.Advance(validity - time.Nanosecond)
	pass(t, f, clock, api.EventModified, newLease("node-a", "2", leaseSeconds))
	assert.Zero(t, f.putCount(), "validity was counted from the old version")
	f.notExpired("node-a")

	clock.Advance(time.Nanosecond)
	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionTrue)
}

func TestARepeatedVersionDoesNotRestartTheValidity(t *testing.T) {
	f, clock := setup(t)

	clock.Advance(8 * time.Second)
	pass(t, f, clock, api.EventModified, newLease("node-a", "1", leaseSeconds))

	clock.Advance(validity - 8*time.Second)
	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionTrue)
}

func TestLeaseExpiredIsClearedWhenTheLeaseIsRenewed(t *testing.T) {
	f, clock := setup(t)
	clock.Advance(validity)
	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionTrue)

	f.emit(api.EventModified, newLease("node-a", "2", leaseSeconds))
	f.waitPuts(2)

	f.expiredIs("node-a", api.ConditionFalse)
	c, _ := f.leaseExpired("node-a")
	assert.Equal(t, "LeaseRenewed", c.Reason)
}

func TestLeaseExpiredIsSetAgainWhenTheRenewedLeaseExpires(t *testing.T) {
	f, clock := setup(t)
	clock.Advance(validity)
	f.waitPuts(1)
	f.emit(api.EventModified, newLease("node-a", "2", leaseSeconds))
	f.waitPuts(2)
	// A pass of its own, so the controller has set its next timer before the clock moves.
	pass(t, f, clock, api.EventModified, newLease("node-a", "2", leaseSeconds))

	clock.Advance(validity)
	f.waitPuts(3)
	f.expiredIs("node-a", api.ConditionTrue)
}

func TestAConditionLeftTrueBeforeStartIsClearedWhileTheLeaseIsValid(t *testing.T) {
	f := newFakeAPI(t)
	n := newNode("node-a")
	n.Status.Conditions = append(n.Status.Conditions, api.Condition{Type: api.NodeLeaseExpired, Status: api.ConditionTrue})
	f.addNode(n)
	f.setLeases("10", newLease("node-a", "1", leaseSeconds))

	start(t, f, newFakeClock())

	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionFalse)
}

func TestAValidLeaseLeavesTheNodeUntouched(t *testing.T) {
	f, clock := setup(t)

	pass(t, f, clock, api.EventModified, newLease("node-a", "2", leaseSeconds))

	assert.Zero(t, f.putCount())
	_, ok := f.leaseExpired("node-a")
	assert.False(t, ok, "a condition was added to a Node whose Lease is valid")
}

func TestAFailedWriteIsRetriedAfterTheRetryDelay(t *testing.T) {
	f, clock := setup(t)
	f.mu.Lock()
	f.failPuts = 1
	f.mu.Unlock()

	before := clock.Calls()
	clock.Advance(validity)
	clock.waitCallsAbove(t, before)
	assert.Zero(t, f.putCount())
	f.notExpired("node-a")

	clock.Advance(nodelifecycle.RetryDelay - time.Nanosecond)
	pass(t, f, clock, api.EventModified, newLease("node-a", "1", leaseSeconds))
	assert.Zero(t, f.putCount(), "retried before the retry delay")

	clock.Advance(time.Nanosecond)
	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionTrue)
}

func TestAConflictingWriteReadsTheNodeAgainAndWrites(t *testing.T) {
	f, clock := setup(t)
	f.mu.Lock()
	f.conflicts = 1
	f.mu.Unlock()

	clock.Advance(validity)

	f.waitPuts(1)
	f.expiredIs("node-a", api.ConditionTrue)
}

func TestAnEndedWatchListsAgainAndPicksUpAMissedRenewal(t *testing.T) {
	f, clock := setup(t)
	clock.Advance(validity)
	f.waitPuts(1)
	lists := f.listCount()

	f.setLeases("20", newLease("node-a", "2", leaseSeconds))
	before := clock.Calls()
	f.endWatch()
	clock.waitCallsAbove(t, before)
	assert.Equal(t, lists, f.listCount(), "listed again before the retry delay")
	clock.Advance(nodelifecycle.RetryDelay)

	f.waitPuts(2)
	f.expiredIs("node-a", api.ConditionFalse)
	assert.Equal(t, lists+1, f.listCount())
}

func TestADeletedLeaseIsForgotten(t *testing.T) {
	f, clock := setup(t)
	f.addNode(newNode("node-b"))
	pass(t, f, clock, api.EventAdded, newLease("node-b", "1", leaseSeconds))

	pass(t, f, clock, api.EventDeleted, newLease("node-a", "1", leaseSeconds))
	clock.Advance(validity)

	f.waitPuts(1)
	f.expiredIs("node-b", api.ConditionTrue)
	f.notExpired("node-a")
}

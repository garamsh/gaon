package scheduler_test

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/scheduler/internal/scheduler"
)

func newService(client scheduler.API) scheduler.Service {
	return scheduler.NewService(client, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func reconcile(t *testing.T, client scheduler.API) {
	t.Helper()
	require.NoError(t, newService(client).Reconcile(context.Background()))
}

func TestReconcile_BindsUnboundMicroVMToReadyNode(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("n1", "4", "8Gi"))
	f.addMicroVM(newMicroVM("m1", "1", "1Gi"))

	reconcile(t, client)

	m := f.microvm("m1")
	assert.Equal(t, "n1", m.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), m.Spec.BindingEpoch)
}

func TestReconcile_ChoosesTheFirstFittingNodeByName(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("a", "1", "1Gi"))
	f.addNode(node("b", "4", "8Gi"))
	f.addNode(node("c", "4", "8Gi"))
	f.addMicroVM(newMicroVM("m1", "2", "1Gi"))

	reconcile(t, client)

	assert.Equal(t, "b", f.microvm("m1").Spec.NodeName, "a is too small, and b sorts before c however the API lists them")
}

func TestReconcile_PlacesOnlyOnAdmittedReadyUnexpiredNode(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(n *api.Node)
	}{
		{"not admitted", func(n *api.Node) { n.Status.State = api.NodeStateDiscovered }},
		{"Ready false", func(n *api.Node) { withCondition(n, api.NodeReady, api.ConditionFalse) }},
		{"Ready unknown", func(n *api.Node) { withCondition(n, api.NodeReady, api.ConditionUnknown) }},
		{"no Ready condition", func(n *api.Node) { n.Status.Conditions = nil }},
		{"LeaseExpired true", func(n *api.Node) { withCondition(n, api.NodeLeaseExpired, api.ConditionTrue) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			n := node("n1", "4", "8Gi")
			tt.mutate(n)
			f.addNode(n)
			f.addMicroVM(newMicroVM("m1", "1", "1Gi"))
			reconcile(t, client)
			assert.Empty(t, f.microvm("m1").Spec.NodeName)
			assert.Zero(t, f.bindingCount())
		})
	}

	t.Run("control: Ready, and LeaseExpired false", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(withCondition(node("n1", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionFalse))
		f.addMicroVM(newMicroVM("m1", "1", "1Gi"))
		reconcile(t, client)
		assert.Equal(t, "n1", f.microvm("m1").Spec.NodeName)
	})
}

func TestReconcile_FitsRequestsInCapacityLessBoundMicroVMs(t *testing.T) {
	t.Run("a MicroVM bound to the Node is charged to it", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("n1", "4", "8Gi"))
		f.addMicroVM(bound(newMicroVM("old", "3", "1Gi"), "n1", 1))
		f.addMicroVM(newMicroVM("big", "2", "1Gi"))
		f.addMicroVM(newMicroVM("small", "1", "1Gi"))
		reconcile(t, client)
		assert.Empty(t, f.microvm("big").Spec.NodeName, "2 cpu does not fit in the 1 cpu left")
		assert.Equal(t, "n1", f.microvm("small").Spec.NodeName, "1 cpu fits in the 1 cpu left")
	})

	t.Run("a MicroVM bound to another Node is not", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("n1", "4", "8Gi"))
		f.addNode(node("n2", "4", "8Gi"))
		f.addMicroVM(bound(newMicroVM("elsewhere", "4", "8Gi"), "n2", 1))
		f.addMicroVM(newMicroVM("m1", "4", "8Gi"))
		reconcile(t, client)
		assert.Equal(t, "n1", f.microvm("m1").Spec.NodeName)
	})

	t.Run("a binding made in the pass is charged to its Node", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("n1", "4", "8Gi"))
		f.addMicroVM(newMicroVM("a", "3", "1Gi"))
		f.addMicroVM(newMicroVM("b", "3", "1Gi"))
		reconcile(t, client)
		assert.Equal(t, "n1", f.microvm("a").Spec.NodeName)
		assert.Empty(t, f.microvm("b").Spec.NodeName)
	})

	t.Run("memory is checked as cpu is", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("n1", "4", "2Gi"))
		f.addMicroVM(newMicroVM("m1", "1", "3Gi"))
		reconcile(t, client)
		assert.Empty(t, f.microvm("m1").Spec.NodeName)
	})
}

func TestReconcile_ComparesQuantitiesByValue(t *testing.T) {
	tests := []struct {
		name                           string
		capCPU, capMem, reqCPU, reqMem string
		fits                           bool
	}{
		{"1 cpu holds 1000m", "1", "1Gi", "1000m", "1Gi", true},
		{"1 cpu does not hold 1001m", "1", "1Gi", "1001m", "1Gi", false},
		{"1.5 cpu holds 1500m", "1.5", "1Gi", "1500m", "1Gi", true},
		{"1Gi holds 1G", "1", "1Gi", "1", "1G", true},
		{"1Gi does not hold 1100M", "1", "1Gi", "1", "1100M", false},
		{"1Gi holds 1024Mi", "1", "1Gi", "1", "1024Mi", true},
		{"1Gi does not hold 1025Mi", "1", "1Gi", "1", "1025Mi", false},
		{"1048576Ki is 1Gi", "1", "1048576Ki", "1", "1Gi", true},
		{"an empty request fits an empty capacity", "", "", "", "", true},
		{"an empty capacity holds no request", "", "1Gi", "1m", "1Gi", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			f.addNode(node("n1", tt.capCPU, tt.capMem))
			f.addMicroVM(newMicroVM("m1", tt.reqCPU, tt.reqMem))
			reconcile(t, client)
			assert.Equal(t, tt.fits, f.microvm("m1").Spec.NodeName == "n1")
		})
	}
}

func TestReconcile_MicroVMThatFitsNoNodeStaysUnboundAndSaysWhy(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("n1", "1", "1Gi"))
	f.addMicroVM(newMicroVM("m1", "2", "1Gi"))

	reconcile(t, client)

	m := f.microvm("m1")
	assert.Empty(t, m.Spec.NodeName)
	assert.Zero(t, m.Spec.BindingEpoch)
	assert.Zero(t, f.bindingCount())
	require.Len(t, m.Status.Conditions, 1)
	c := m.Status.Conditions[0]
	assert.Equal(t, scheduler.ConditionScheduled, c.Type)
	assert.Equal(t, api.ConditionFalse, c.Status)
	assert.Equal(t, scheduler.ReasonUnschedulable, c.Reason)
	assert.Contains(t, c.Message, "1 of 1 Nodes are Admitted, Ready and not LeaseExpired")
	assert.Contains(t, c.Message, `cpu "2"`)

	t.Run("a pass that finds the same reason writes nothing", func(t *testing.T) {
		reconcile(t, client)
		assert.Equal(t, 1, f.statusPutCount())
	})

	t.Run("a Node with room binds it", func(t *testing.T) {
		f.addNode(node("n2", "4", "8Gi"))
		reconcile(t, client)
		assert.Equal(t, "n2", f.microvm("m1").Spec.NodeName)
	})
}

func TestReconcile_UnschedulableMessageCountsEligibleNodes(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(withCondition(node("n1", "4", "8Gi"), api.NodeReady, api.ConditionFalse))
	f.addMicroVM(newMicroVM("m1", "1", "1Gi"))

	reconcile(t, client)

	require.Len(t, f.microvm("m1").Status.Conditions, 1)
	assert.Contains(t, f.microvm("m1").Status.Conditions[0].Message, "0 of 1 Nodes are Admitted, Ready and not LeaseExpired")
}

func TestReconcile_EachBindingTakesTheNextEpoch(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("n1", "4", "8Gi"))
	f.addNode(withCondition(node("dead", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionTrue))
	f.addMicroVM(newMicroVM("first", "1", "1Gi"))
	f.addMicroVM(bound(newMicroVM("again", "1", "1Gi"), "dead", 3))

	reconcile(t, client)

	assert.Equal(t, api.Epoch(1), f.microvm("first").Spec.BindingEpoch)
	assert.Equal(t, api.Epoch(4), f.microvm("again").Spec.BindingEpoch)
	assert.Equal(t, "n1", f.microvm("again").Spec.NodeName)
}

func TestReconcile_RebindsOnlyAfterLeaseExpiryIsConfirmed(t *testing.T) {
	tests := []struct {
		name  string
		nodeA func() *api.Node // nil: the Node does not exist
	}{
		{"no LeaseExpired condition", func() *api.Node { return node("a", "4", "8Gi") }},
		{"LeaseExpired false", func() *api.Node {
			return withCondition(node("a", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionFalse)
		}},
		{"LeaseExpired unknown", func() *api.Node {
			return withCondition(node("a", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionUnknown)
		}},
		{"Ready false but no expiry confirmed", func() *api.Node { return withCondition(node("a", "4", "8Gi"), api.NodeReady, api.ConditionFalse) }},
		{"the Node is gone", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			if tt.nodeA != nil {
				f.addNode(tt.nodeA())
			}
			f.addNode(node("b", "4", "8Gi"))
			f.addMicroVM(bound(newMicroVM("m1", "1", "1Gi"), "a", 1))
			reconcile(t, client)
			m := f.microvm("m1")
			assert.Equal(t, "a", m.Spec.NodeName)
			assert.Equal(t, api.Epoch(1), m.Spec.BindingEpoch)
			assert.Zero(t, f.bindingCount())
		})
	}

	t.Run("control: LeaseExpired true", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(withCondition(node("a", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionTrue))
		f.addNode(node("b", "4", "8Gi"))
		f.addMicroVM(bound(newMicroVM("m1", "1", "1Gi"), "a", 1))
		reconcile(t, client)
		m := f.microvm("m1")
		assert.Equal(t, "b", m.Spec.NodeName)
		assert.Equal(t, api.Epoch(2), m.Spec.BindingEpoch)
	})
}

func TestReconcile_RebindThatFitsNoNodeLeavesTheMicroVMAlone(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(withCondition(node("a", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionTrue))
	f.addNode(node("b", "1", "1Gi"))
	f.addMicroVM(bound(newMicroVM("m1", "2", "1Gi"), "a", 1))

	reconcile(t, client)

	m := f.microvm("m1")
	assert.Equal(t, "a", m.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), m.Spec.BindingEpoch)
	assert.Zero(t, f.statusPutCount(), "the dead Node's epoch status is nobody's to rewrite")
}

func TestReconcile_LeavesDeletingMicroVMAlone(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("n1", "4", "8Gi"))
	now := time.Now()
	m := newMicroVM("m1", "1", "1Gi")
	m.Metadata.DeletionTimestamp = &now
	f.addMicroVM(m)

	reconcile(t, client)

	assert.Empty(t, f.microvm("m1").Spec.NodeName)
	assert.Zero(t, f.statusPutCount())
}

func TestReconcile_RacingSchedulersProduceOneBinding(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("n1", "4", "8Gi"))
	f.addMicroVM(newMicroVM("m1", "1", "1Gi"))
	before := f.microvm("m1").Metadata.ResourceVersion
	f.holdBindings(2)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = newService(client).Reconcile(context.Background())
		}()
	}
	wg.Wait()

	assert.NoError(t, errs[0])
	assert.NoError(t, errs[1], "a lost race is not a failure")
	assert.Equal(t, 2, f.bindingCount(), "both schedulers attempted the binding")
	m := f.microvm("m1")
	assert.Equal(t, "n1", m.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), m.Spec.BindingEpoch)
	oldRV, err := strconv.Atoi(string(before))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(oldRV+1), string(m.Metadata.ResourceVersion), "one write changed the MicroVM")
}

func TestRun_ReconcilesEachIntervalUntilCancelled(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("n1", "4", "8Gi"))
	f.addMicroVM(newMicroVM("first", "1", "1Gi"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		newService(client).Run(ctx, 10*time.Millisecond)
	}()

	assert.Eventually(t, func() bool { return f.microvm("first").Spec.NodeName == "n1" }, 5*time.Second, 5*time.Millisecond)
	f.addMicroVM(newMicroVM("second", "1", "1Gi"))
	assert.Eventually(t, func() bool { return f.microvm("second").Spec.NodeName == "n1" }, 5*time.Second, 5*time.Millisecond, "a MicroVM created later is bound by a later pass")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

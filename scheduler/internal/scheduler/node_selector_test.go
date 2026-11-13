package scheduler_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/scheduler/internal/scheduler"
)

// withLabels sets the labels of n.
func withLabels(n *api.Node, labels map[string]string) *api.Node {
	n.Metadata.Labels = labels
	return n
}

// withNodeSelector sets the nodeSelector of m.
func withNodeSelector(m *api.MicroVM, selector map[string]string) *api.MicroVM {
	m.Spec.NodeSelector = selector
	return m
}

func TestNodeSelector_KeepsOnlyNodesWhoseLabelsHoldEveryPair(t *testing.T) {
	cp := map[string]string{"gaon.garam.sh/control-plane": "true"}

	tests := []struct {
		name     string
		selector map[string]string
		labels   map[string]string // the labels of Node b; Node a has none
		wantOn   string            // empty when no Node matches
		message  string
	}{
		{name: "absent selector matches every Node", selector: nil, labels: nil, wantOn: "a"},
		{name: "empty selector matches every Node", selector: map[string]string{}, labels: cp, wantOn: "a"},
		{name: "control: one pair held by b", selector: cp, labels: cp, wantOn: "b"},
		{
			name:     "a pair whose value differs",
			selector: cp,
			labels:   map[string]string{"gaon.garam.sh/control-plane": "false"},
			message:  "NodeSelector refused 2 (labels do not hold nodeSelector gaon.garam.sh/control-plane=true)",
		},
		{
			name:     "a pair whose key is absent",
			selector: cp,
			labels:   map[string]string{"zone": "x"},
			message:  "NodeSelector refused 2 (labels do not hold nodeSelector gaon.garam.sh/control-plane=true)",
		},
		{
			name:     "a key held with an empty value is not a key held with another",
			selector: map[string]string{"k": ""},
			labels:   map[string]string{"k": "v"},
			message:  "NodeSelector refused 2 (labels do not hold nodeSelector k=)",
		},
		{
			name:     "control: an empty value held matches an empty value",
			selector: map[string]string{"k": ""},
			labels:   map[string]string{"k": ""},
			wantOn:   "b",
		},
		{
			name:     "two pairs, one held",
			selector: map[string]string{"zone": "x", "disk": "ssd"},
			labels:   map[string]string{"zone": "x"},
			message:  "NodeSelector refused 2 (labels do not hold nodeSelector disk=ssd,zone=x)",
		},
		{
			name:     "control: two pairs, both held and another label besides",
			selector: map[string]string{"zone": "x", "disk": "ssd"},
			labels:   map[string]string{"zone": "x", "disk": "ssd", "other": "y"},
			wantOn:   "b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			f.addNode(node("a", "4", "8Gi"))
			f.addNode(withLabels(node("b", "4", "8Gi"), tt.labels))
			f.addMicroVM(withNodeSelector(newMicroVM("m1", "1", "1Gi"), tt.selector))

			reconcile(t, client)

			m := f.microvm("m1")
			assert.Equal(t, tt.wantOn, m.Spec.NodeName)
			if tt.wantOn != "" {
				assert.Equal(t, 1, f.bindingCount())
				return
			}
			assert.Zero(t, f.bindingCount())
			require.Len(t, m.Status.Conditions, 1)
			assert.Equal(t, scheduler.ReasonUnschedulable, m.Status.Conditions[0].Reason)
			assert.Contains(t, m.Status.Conditions[0].Message, tt.message)
		})
	}
}

func TestNodeSelector_TheMatchingNodeStillHasToFit(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(withLabels(node("a", "1", "1Gi"), map[string]string{"zone": "x"}))
	f.addNode(node("b", "4", "8Gi"))
	f.addMicroVM(withNodeSelector(newMicroVM("m1", "2", "1Gi"), map[string]string{"zone": "x"}))

	reconcile(t, client)

	m := f.microvm("m1")
	assert.Empty(t, m.Spec.NodeName, "a matches and is too small; b is large enough and does not match")
	require.Len(t, m.Status.Conditions, 1)
	assert.Contains(t, m.Status.Conditions[0].Message, "NodeResourcesFit refused 1")
	assert.Contains(t, m.Status.Conditions[0].Message, "NodeSelector refused 1")
}

func TestNodeSelector_AMicroVMWhoseNodeExpiredIsReboundOnlyToAMatchingNode(t *testing.T) {
	setup := func(t *testing.T, labels map[string]string) (*fakeAPI, api.MicroVM) {
		f, client := newFakeAPI(t)
		f.addNode(withCondition(withLabels(node("n1", "4", "8Gi"), map[string]string{"zone": "x"}), api.NodeLeaseExpired, api.ConditionTrue))
		f.addNode(withLabels(node("n2", "4", "8Gi"), labels))
		f.addMicroVM(withNodeSelector(bound(newMicroVM("m1", "1", "1Gi"), "n1", 1), map[string]string{"zone": "x"}))
		reconcile(t, client)
		return f, f.microvm("m1")
	}

	f, m := setup(t, map[string]string{"zone": "y"})
	assert.Equal(t, "n1", m.Spec.NodeName)
	assert.Zero(t, f.bindingCount())

	_, m = setup(t, map[string]string{"zone": "x"})
	assert.Equal(t, "n2", m.Spec.NodeName, "control: the same MicroVM is rebound to a Node that matches")
	assert.Equal(t, api.Epoch(2), m.Spec.BindingEpoch)
}

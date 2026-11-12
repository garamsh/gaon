package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newNode(name string) *api.Node {
	return &api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: name},
	}
}

func TestNodeValidateStatusRefusesAnUnknownState(t *testing.T) {
	for _, state := range []api.NodeState{api.NodeStateDiscovered, api.NodeStateAdmitted} {
		n := newNode("node-a")
		n.Status.State = state
		assert.NoError(t, n.ValidateStatus(), string(state))
	}
	for _, state := range []api.NodeState{"", "Ready", "admitted"} {
		n := newNode("node-a")
		n.Status.State = state
		assert.ErrorIs(t, n.ValidateStatus(), api.ErrInvalid, "%q", state)
	}
}

func TestNodeValidateStatusRefusesAnUnknownConditionStatus(t *testing.T) {
	for _, status := range []api.ConditionStatus{api.ConditionTrue, api.ConditionFalse, api.ConditionUnknown} {
		n := newNode("node-a")
		n.Status = api.NodeStatus{
			State:      api.NodeStateAdmitted,
			Conditions: []api.Condition{{Type: api.NodeReady, Status: api.ConditionTrue}, {Type: api.NodeLeaseExpired, Status: status}},
		}
		assert.NoError(t, n.ValidateStatus(), string(status))
	}
	for _, status := range []api.ConditionStatus{"", "true", "Maybe"} {
		n := newNode("node-a")
		n.Status = api.NodeStatus{
			State:      api.NodeStateAdmitted,
			Conditions: []api.Condition{{Type: api.NodeReady, Status: api.ConditionTrue}, {Type: api.NodeLeaseExpired, Status: status}},
		}
		assert.ErrorIs(t, n.ValidateStatus(), api.ErrInvalid, "%q", status)
	}
}

func TestNodeValidateStatusRefusesAMicroVMPrefixThatIsNotACIDR(t *testing.T) {
	n := newNode("node-a")
	n.Status.State = api.NodeStateAdmitted
	n.Status.MicroVMPrefixes = []string{"10.1.0.0/16", "fd00:1::/64"}
	assert.NoError(t, n.ValidateStatus(), "control: valid IPv4 and IPv6 prefixes are accepted")
	for _, bad := range []string{"", "10.1.0.0", "10.1.0.0/33", "not-a-prefix"} {
		n.Status.MicroVMPrefixes = []string{"10.1.0.0/16", bad}
		assert.ErrorIs(t, n.ValidateStatus(), api.ErrInvalid, "%q", bad)
	}
}

func TestNodeSpecWritesAdmittedWhenFalse(t *testing.T) {
	raw, err := json.Marshal(newNode("node-a"))
	require.NoError(t, err)
	var decoded struct {
		Spec map[string]any `json:"spec"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	admitted, present := decoded.Spec["admitted"]
	assert.True(t, present, "a Node created with an empty spec states admitted")
	assert.Equal(t, false, admitted)
}

func TestClientNodeRegistersIsAdmittedAndReportsStatus(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	registered, err := c.PutNode(ctx, newNode("node-a"))
	require.NoError(t, err)
	assert.False(t, registered.Spec.Admitted)
	assert.Equal(t, api.NodeStateDiscovered, registered.Status.State)

	_, err = c.PutNode(ctx, newNode("node-a"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "registering again creates nothing")

	admit := *registered
	admit.Spec.Admitted = true
	admitted, err := c.PutNode(ctx, &admit)
	require.NoError(t, err)
	assert.True(t, admitted.Spec.Admitted)
	assert.Equal(t, api.NodeStateDiscovered, admitted.Status.State, "the Node Agent writes the state, not the spec write")

	report := *admitted
	report.Status = api.NodeStatus{
		State:      api.NodeStateAdmitted,
		Capacity:   api.NodeCapacity{CPU: "8", Memory: "32Gi", Storage: "500Gi"},
		Addresses:  []string{"10.0.0.7"},
		Conditions: []api.Condition{{Type: api.NodeReady, Status: api.ConditionTrue}},
	}
	reported, err := c.PutNodeStatus(ctx, &report)
	require.NoError(t, err)
	assert.Equal(t, report.Status, reported.Status)

	expire := *reported
	expire.Status.Conditions = append(append([]api.Condition{}, reported.Status.Conditions...),
		api.Condition{Type: api.NodeLeaseExpired, Status: api.ConditionTrue})
	expired, err := c.PutNodeStatus(ctx, &expire)
	require.NoError(t, err)
	assert.Len(t, expired.Status.Conditions, 2, "the controller's write keeps the Node Agent's condition")
	assert.Equal(t, report.Status.Capacity, expired.Status.Capacity)

	_, err = c.PutNodeStatus(ctx, &report)
	assert.ErrorIs(t, err, api.ErrConflict, "a status write from an older version loses the compare-and-swap")

	got, err := c.GetNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, expired, got)
}

func TestClientNodeStatusWriteRefusesWhatIsNotAStatus(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	registered, err := c.PutNode(ctx, newNode("node-a"))
	require.NoError(t, err)

	badState := *registered
	badState.Status.State = "Ready"
	_, err = c.PutNodeStatus(ctx, &badState)
	assert.ErrorIs(t, err, api.ErrInvalid)

	badCondition := *registered
	badCondition.Status.State = api.NodeStateAdmitted
	badCondition.Status.Conditions = []api.Condition{{Type: api.NodeReady, Status: "Maybe"}}
	_, err = c.PutNodeStatus(ctx, &badCondition)
	assert.ErrorIs(t, err, api.ErrInvalid)

	control := *registered
	control.Status.State = api.NodeStateAdmitted
	control.Status.Conditions = []api.Condition{{Type: api.NodeReady, Status: api.ConditionTrue}}
	_, err = c.PutNodeStatus(ctx, &control)
	assert.NoError(t, err, "the same write with known values is accepted")
}

func TestClientNodeGetAndList(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetNode(ctx, "absent")
	assert.ErrorIs(t, err, api.ErrNotFound)

	_, err = c.PutNode(ctx, newNode("node-a"))
	require.NoError(t, err)
	second, err := c.PutNode(ctx, newNode("node-b"))
	require.NoError(t, err)

	list, err := c.ListNodes(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindNodeList, list.Kind)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, second.Metadata.ResourceVersion, list.Metadata.ResourceVersion)
}

func TestClientWatchNodesDeliversNodeEventsOnly(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	_, err := c.PutMicroVM(ctx, newMicroVM("m1"))
	require.NoError(t, err)
	registered, err := c.PutNode(ctx, newNode("node-a"))
	require.NoError(t, err)

	w, err := c.WatchNodes(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "node-a", ev.Object.Metadata.Name, "the MicroVM's event is not delivered to a Node watch")

	admit := *registered
	admit.Spec.Admitted = true
	changed, err := c.PutNode(ctx, &admit)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Equal(t, changed.Metadata.ResourceVersion, ev.Object.Metadata.ResourceVersion)
	assert.True(t, ev.Object.Spec.Admitted)

	s.compact()
	stale, err := c.WatchNodes(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)
	_, err = stale.Next()
	assert.ErrorIs(t, err, io.EOF, "the Error event is the last of its stream")

	_, err = c.WatchNodes(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}

func TestWatchRefusesAnEventOfAnotherKind(t *testing.T) {
	microvm := `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"MicroVM","metadata":{"name":"m1"}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"type":"Added","object":`+microvm+"}\n")
	}))
	t.Cleanup(ts.Close)
	c := api.NewClient(ts.URL, ts.Client())

	nodes, err := c.WatchNodes(testContext(t), "1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = nodes.Close() })
	_, err = nodes.Next()
	require.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF)

	microvms, err := c.WatchMicroVMs(testContext(t), "1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = microvms.Close() })
	assert.Equal(t, "m1", nextEvent(t, microvms).Object.Metadata.Name, "control: a MicroVM watch reads the same line")
}

func TestWatchWithoutAVersionSendsNoRequest(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(ts.Close)
	c := api.NewClient(ts.URL, ts.Client())
	ctx := testContext(t)

	_, err := c.WatchMicroVMs(ctx, "")
	assert.Error(t, err)
	_, err = c.WatchNodes(ctx, "")
	assert.Error(t, err)
	_, err = c.WatchLeases(ctx, "")
	assert.Error(t, err)
	assert.Zero(t, requests.Load(), "the version is required before anything is sent")

	w, err := c.WatchNodes(ctx, "1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	assert.Equal(t, int32(1), requests.Load(), "control: with a version the request is sent")
}

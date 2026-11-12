package node_test

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/garamsh/gaon/node-agent/internal/node"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// flaky is mvmd's runtime info that fails while down is set.
type flaky struct {
	node.Runtime
	down bool
}

func (f *flaky) GetRuntimeInfo(ctx context.Context, in *v1alpha2.GetRuntimeInfoRequest, opts ...grpc.CallOption) (*v1alpha2.GetRuntimeInfoResponse, error) {
	if f.down {
		return nil, errors.New("mvmd socket closed")
	}
	return f.Runtime.GetRuntimeInfo(ctx, in, opts...)
}

func newService(t *testing.T) (node.Service, *fakegaon.Server, *clock.Fake) {
	svc, gaon, clk, _ := newFlakyService(t)
	return svc, gaon, clk
}

func newFlakyService(t *testing.T) (node.Service, *fakegaon.Server, *clock.Fake, *flaky) {
	svc, gaon, _, clk, rt := newLoggingService(t, io.Discard)
	return svc, gaon, clk, rt
}

// newLoggingService is newFlakyService with the agent's log written to w, and
// the fake mvmd returned.
func newLoggingService(t *testing.T, w io.Writer) (node.Service, *fakegaon.Server, *fakemvmd.Server, *clock.Fake, *flaky) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	gaon, mvmd := fakegaon.New(t), fakemvmd.New(t, clk)
	conn, err := runtimeapi.Dial(context.Background(), mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	rt := &flaky{Runtime: v1alpha2.NewRuntimeServiceClient(conn)}
	svc := node.NewService("n1", api.NewClient(gaon.URL, nil), rt, clk, slog.New(slog.NewTextHandler(w, nil)))
	return svc, gaon, mvmd, clk, rt
}

func TestSync_CreatesTheNodeWithAnEmptySpecAndCapacityFromRuntimeInfo(t *testing.T) {
	svc, gaon, _ := newService(t)
	_, existed := gaon.Node("n1")
	require.False(t, existed, "setup: the API has no Node yet")

	require.NoError(t, svc.Sync(context.Background()))

	n, ok := gaon.Node("n1")
	require.True(t, ok)
	assert.Equal(t, api.NodeSpec{}, n.Spec, "the agent creates the Node with an empty spec")
	assert.Equal(t, api.NodeCapacity{CPU: "8", Memory: "17179869184", Storage: "107374182400"}, n.Status.Capacity)
	assert.Equal(t, api.NodeStateDiscovered, n.Status.State, "an unadmitted Node stays Discovered")
	require.Len(t, n.Status.Conditions, 1)
	assert.Equal(t, api.NodeReady, n.Status.Conditions[0].Type)
	assert.Equal(t, api.ConditionTrue, n.Status.Conditions[0].Status)
}

func TestSync_ReportsAdmittedOnceAUserAdmitsTheNodeAndKeepsTheirSpec(t *testing.T) {
	svc, gaon, _ := newService(t)
	require.NoError(t, svc.Sync(context.Background()))
	gaon.SetNodeAdmitted("n1", true)

	require.NoError(t, svc.Sync(context.Background()))

	n, _ := gaon.Node("n1")
	assert.Equal(t, api.NodeStateAdmitted, n.Status.State)
	assert.True(t, n.Spec.Admitted, "the agent writes status only; spec.admitted stays as the user set it")
}

func TestSync_KeepsTheReadyTransitionTimeWhileReadyStaysTrue(t *testing.T) {
	svc, gaon, clk := newService(t)
	require.NoError(t, svc.Sync(context.Background()))
	first, _ := gaon.Node("n1")
	clk.Advance(time.Minute)
	require.NoError(t, svc.Sync(context.Background()))
	second, _ := gaon.Node("n1")
	require.Len(t, second.Status.Conditions, 1)
	assert.Equal(t, first.Status.Conditions[0].LastTransitionTime, second.Status.Conditions[0].LastTransitionTime)
}

func ready(t *testing.T, gaon *fakegaon.Server) api.Condition {
	t.Helper()
	n, ok := gaon.Node("n1")
	require.True(t, ok)
	for _, c := range n.Status.Conditions {
		if c.Type == api.NodeReady {
			return c
		}
	}
	t.Fatal("the Node has no Ready condition")
	return api.Condition{}
}

func TestSync_WritesReadyFalseWhileTheRuntimeDoesNotAnswerAndTrueAgainWhenItDoes(t *testing.T) {
	svc, gaon, clk, rt := newFlakyService(t)
	require.NoError(t, svc.Sync(context.Background()))
	first := ready(t, gaon)
	require.Equal(t, api.ConditionTrue, first.Status, "control: with the runtime answering the Node is Ready")
	stored, _ := gaon.Node("n1")

	rt.down = true
	clk.Advance(time.Minute)
	require.Error(t, svc.Sync(context.Background()), "Sync reports the runtime failure")
	down := ready(t, gaon)
	assert.Equal(t, api.ConditionFalse, down.Status)
	assert.Equal(t, node.ReasonRuntimeUnreachable, down.Reason)
	assert.Contains(t, down.Message, "mvmd socket closed", "the error is the message")
	n, _ := gaon.Node("n1")
	assert.Equal(t, stored.Status.Capacity, n.Status.Capacity, "the stored capacity is kept")
	assert.True(t, down.LastTransitionTime.After(*first.LastTransitionTime), "Ready changed, so the transition time moved")

	clk.Advance(time.Minute)
	require.Error(t, svc.Sync(context.Background()))
	again := ready(t, gaon)
	assert.Equal(t, down.LastTransitionTime, again.LastTransitionTime, "the transition time is kept while status and reason are unchanged")

	rt.down = false
	clk.Advance(time.Minute)
	require.NoError(t, svc.Sync(context.Background()))
	back := ready(t, gaon)
	assert.Equal(t, api.ConditionTrue, back.Status, "the same Node is Ready again once the runtime answers")
	assert.True(t, back.LastTransitionTime.After(*again.LastTransitionTime))
}

func TestSync_CreatesTheNodeNotReadyWhenTheRuntimeHasNeverAnswered(t *testing.T) {
	svc, gaon, _, rt := newFlakyService(t)
	rt.down = true
	require.Error(t, svc.Sync(context.Background()))
	assert.Equal(t, api.ConditionFalse, ready(t, gaon).Status)
}

func TestSync_ReportsTheMicroVMPrefixesMvmdReportsAndNoneWhenItReportsNone(t *testing.T) {
	svc, gaon, mvmd, clk, _ := newLoggingService(t, io.Discard)
	mvmd.SetMachinePrefixes("10.1.0.0/16", "fd00:1::/64")
	require.NoError(t, svc.Sync(context.Background()))
	n, _ := gaon.Node("n1")
	assert.Equal(t, []string{"10.1.0.0/16", "fd00:1::/64"}, n.Status.MicroVMPrefixes)

	mvmd.SetMachinePrefixes()
	clk.Advance(time.Minute)
	require.NoError(t, svc.Sync(context.Background()))
	n, _ = gaon.Node("n1")
	assert.Empty(t, n.Status.MicroVMPrefixes, "a Node whose mvmd reports none shows none")
}

func TestSync_KeepsTheMicroVMPrefixesWhileTheRuntimeDoesNotAnswer(t *testing.T) {
	svc, gaon, mvmd, clk, rt := newLoggingService(t, io.Discard)
	mvmd.SetMachinePrefixes("10.1.0.0/16")
	require.NoError(t, svc.Sync(context.Background()))

	rt.down = true
	clk.Advance(time.Minute)
	require.Error(t, svc.Sync(context.Background()))
	n, _ := gaon.Node("n1")
	assert.Equal(t, []string{"10.1.0.0/16"}, n.Status.MicroVMPrefixes)
}

func TestSync_RefusesAMicroVMPrefixThatIsNotACIDRAndNamesItInTheLog(t *testing.T) {
	var log bytes.Buffer
	svc, gaon, mvmd, _, _ := newLoggingService(t, &log)
	mvmd.SetMachinePrefixes("10.1.0.0/16", "10.2.0.0/33", "10.3.0.0")
	require.NoError(t, svc.Sync(context.Background()))

	n, _ := gaon.Node("n1")
	assert.Equal(t, []string{"10.1.0.0/16"}, n.Status.MicroVMPrefixes, "the valid prefix is written, the others are not")
	assert.Contains(t, log.String(), "10.2.0.0/33")
	assert.Contains(t, log.String(), "10.3.0.0")
	assert.NotContains(t, log.String(), "prefix=10.1.0.0/16", "control: a valid prefix is not logged as refused")
}

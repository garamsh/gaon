package node_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/node"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

func newService(t *testing.T) (node.Service, *fakegaon.Server, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	gaon, mvmd := fakegaon.New(t), fakemvmd.New(t, clk)
	conn, err := runtimeapi.Dial(context.Background(), mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	svc := node.NewService("n1", api.NewClient(gaon.URL, nil), v1alpha2.NewRuntimeServiceClient(conn), clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return svc, gaon, clk
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

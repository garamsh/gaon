package machines_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const node = "n1"

var image = "registry.example/app@sha256:" + strings.Repeat("a", 64)

type env struct {
	gaon *fakegaon.Server
	mvmd *fakemvmd.Server
	rt   v1alpha2.RuntimeServiceClient
}

// start runs the service against fakes, with the fencing deadline armed
// unless armed is false, and stops it when the test ends.
func start(t *testing.T, armed bool, wrap func(machines.API) machines.API, setup ...func(*env)) *env {
	t.Helper()
	clk := clock.System{}
	e := &env{gaon: fakegaon.New(t), mvmd: fakemvmd.New(t, clk)}
	for _, f := range setup {
		f(e)
	}
	conn, err := runtimeapi.Dial(context.Background(), e.mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	e.rt = v1alpha2.NewRuntimeServiceClient(conn)
	if armed {
		e.mvmd.Arm(time.Hour, 1)
	}
	var client machines.API = api.NewClient(e.gaon.URL, nil)
	if wrap != nil {
		client = wrap(client)
	}
	svc := machines.NewService(node, machines.Params{Resync: 20 * time.Millisecond, Retry: 20 * time.Millisecond},
		client, e.rt, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return e
}

func machine(name string, epoch api.Epoch, nodeName string, strategy api.RunStrategy) api.Machine {
	return api.Machine{
		Metadata: api.ObjectMeta{Name: name, Generation: 1},
		Spec: api.MachineSpec{
			Image:        image,
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "2", Memory: "1Gi"}},
			RunStrategy:  strategy,
			NodeName:     nodeName,
			BindingEpoch: epoch,
		},
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 10*time.Millisecond, what)
}

func (e *env) runtimeState(id string) v1alpha2.MachineState {
	m, ok := e.mvmd.Machine(id)
	if !ok {
		return v1alpha2.MachineState_MACHINE_STATE_UNSPECIFIED
	}
	return m.GetState()
}

func (e *env) running(id string) func() bool {
	return func() bool { return e.runtimeState(id) == v1alpha2.MachineState_MACHINE_STATE_RUNNING }
}

func (e *env) phase(name string, epoch api.Epoch, phase api.MachinePhase) func() bool {
	return func() bool {
		s := e.gaon.Machine(name).Status
		return s.BindingEpoch == epoch && s.Phase == phase
	}
}

func (e *env) gone(id string) func() bool {
	return func() bool { return !slices.Contains(e.mvmd.IDs(), id) }
}

func TestRun_CreatesAndStartsAMachineBoundToThisNodeAtItsEpoch(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))

	eventually(t, "mvmd runs m1-e1", e.running("m1-e1"))
	eventually(t, "status Running at epoch 1", e.phase("m1", 1, api.MachineRunning))

	got, _ := e.mvmd.Machine("m1-e1")
	assert.EqualValues(t, 2, got.GetResources().GetCpuCount())
	assert.EqualValues(t, 1<<30, got.GetResources().GetMemoryBytes())
	assert.Equal(t, []string{strings.Repeat("a", 64)}, trimDigests(e.mvmd.Images()))
	s := e.gaon.Machine("m1").Status
	assert.Equal(t, []string{"10.0.0.2"}, s.Addresses)
	assert.EqualValues(t, 1, s.ObservedGeneration)
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, api.ConditionTrue, s.Conditions[0].Status)
}

func trimDigests(in []string) []string {
	out := make([]string, len(in))
	for i, d := range in {
		out[i] = strings.TrimPrefix(d, "sha256:")
	}
	return out
}

func TestRun_IgnoresAMachineBoundToAnotherNode(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("other", 1, "n2", api.RunAlways))
	e.gaon.SetMachine(machine("mine", 1, node, api.RunAlways))

	eventually(t, "control: mine runs", e.running("mine-e1"))
	assert.NotContains(t, e.mvmd.IDs(), "other-e1")
	assert.Empty(t, e.gaon.Machine("other").Status.Phase, "the agent writes no status of a Machine that is not bound to it")
}

func TestRun_StopsAndRemovesAMachineReboundElsewhere(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("keep", 1, node, api.RunAlways))
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))
	eventually(t, "m1 runs", e.running("m1-e1"))

	e.gaon.SetMachine(machine("m1", 2, "n2", api.RunAlways))

	eventually(t, "m1-e1 is removed from mvmd", e.gone("m1-e1"))
	assert.Contains(t, e.mvmd.Calls(), "StopMachine m1-e1", "it is stopped before it is deleted")
	assert.Equal(t, v1alpha2.MachineState_MACHINE_STATE_RUNNING, e.runtimeState("keep-e1"), "control: a Machine still bound here is left running")
}

func TestRun_StopsAndRemovesAMachineUnboundOrDeleted(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("unbound", 1, node, api.RunAlways))
	e.gaon.SetMachine(machine("deleted", 1, node, api.RunAlways))
	e.gaon.SetMachine(machine("keep", 1, node, api.RunAlways))
	eventually(t, "all run", func() bool {
		return e.running("unbound-e1")() && e.running("deleted-e1")() && e.running("keep-e1")()
	})

	e.gaon.SetMachine(machine("unbound", 1, "", api.RunAlways))
	e.gaon.DeleteMachine("deleted")

	eventually(t, "unbound-e1 is removed", e.gone("unbound-e1"))
	eventually(t, "deleted-e1 is removed", e.gone("deleted-e1"))
	assert.True(t, e.running("keep-e1")(), "control: the Machine still bound here runs")
}

func TestRun_ReplacesTheMachineWhenReboundToThisNodeAtALaterEpoch(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs", e.running("m1-e1"))

	e.gaon.SetMachine(machine("m1", 2, node, api.RunAlways))

	eventually(t, "m1-e2 runs", e.running("m1-e2"))
	eventually(t, "m1-e1 is removed", e.gone("m1-e1"))
	eventually(t, "status is that of epoch 2", e.phase("m1", 2, api.MachineRunning))
}

func TestRun_IgnoresABindingOlderThanTheNewestEpochSeen(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("m1", 2, node, api.RunAlways))
	eventually(t, "m1-e2 runs", e.running("m1-e2"))

	// A delayed event shows the Machine bound here at epoch 1, then a later
	// Machine proves the stream was read past it.
	e.gaon.Inject(api.EventModified, machine("m1", 1, node, api.RunAlways))
	e.gaon.SetMachine(machine("marker", 1, node, api.RunAlways))
	eventually(t, "marker runs, so the stale event has been read", e.running("marker-e1"))

	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine m1-e1")
	assert.True(t, e.running("m1-e2")(), "the newest binding is left running")
	assert.EqualValues(t, 2, e.gaon.Machine("m1").Spec.BindingEpoch)
}

func TestRun_ReportsPhaseFromMvmdState(t *testing.T) {
	tests := []struct {
		name     string
		strategy api.RunStrategy
		state    v1alpha2.MachineState
		reason   string
		phase    api.MachinePhase
		cond     string
		message  string
	}{
		{"a faulted VMM is Failed with mvmd's reason", api.RunAlways, v1alpha2.MachineState_MACHINE_STATE_FAULTED, "vmm exited: oom", api.MachineFailed, machines.ReasonFaulted, "vmm exited: oom"},
		{"a Once Machine that stopped Succeeded", api.RunOnce, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "", api.MachineSucceeded, machines.ReasonStopped, ""},
		{"a paused Machine is Suspended", api.RunAlways, v1alpha2.MachineState_MACHINE_STATE_PAUSED, "", api.MachineSuspended, machines.ReasonSuspended, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := start(t, true, nil)
			e.gaon.SetMachine(machine("m1", 1, node, tt.strategy))
			eventually(t, "status Running", e.phase("m1", 1, api.MachineRunning))

			e.mvmd.SetState("m1-e1", tt.state, tt.reason)

			eventually(t, "phase "+string(tt.phase), e.phase("m1", 1, tt.phase))
			s := e.gaon.Machine("m1").Status
			require.Len(t, s.Conditions, 1)
			assert.Equal(t, machines.ConditionReady, s.Conditions[0].Type)
			assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
			assert.Equal(t, tt.cond, s.Conditions[0].Reason)
			assert.Equal(t, tt.message, s.Conditions[0].Message)
		})
	}
}

func TestRun_StopsAHaltedMachineAndStartsItAgainWhenResumed(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs", e.running("m1-e1"))

	e.gaon.SetMachine(machine("m1", 1, node, api.RunHalted))
	eventually(t, "status Stopped", e.phase("m1", 1, api.MachineStopped))
	assert.Equal(t, v1alpha2.MachineState_MACHINE_STATE_STOPPED, e.runtimeState("m1-e1"))

	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs again", e.running("m1-e1"))
	eventually(t, "status Running", e.phase("m1", 1, api.MachineRunning))
}

func TestRun_ReportsFailedWhenMvmdRejectsTheCreateAboveMaxVCPUs(t *testing.T) {
	e := start(t, true, nil, func(e *env) { e.mvmd.MaxVCPUs = 2 })
	over := machine("over", 1, node, api.RunAlways)
	over.Spec.Resources.Limits.CPU = "4"
	e.gaon.SetMachine(over)
	e.gaon.SetMachine(machine("within", 1, node, api.RunAlways))

	eventually(t, "status Failed", e.phase("over", 1, api.MachineFailed))
	eventually(t, "control: a Machine at the limit runs", e.running("within-e1"))

	s := e.gaon.Machine("over").Status
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, machines.ReasonRejected, s.Conditions[0].Reason)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	assert.Contains(t, s.Conditions[0].Message, "max_vcpus", "the condition carries mvmd's reason")
	assert.NotContains(t, e.mvmd.IDs(), "over-e1")
}

func TestRun_ReportsFailedForAnImageWithoutADigest(t *testing.T) {
	e := start(t, true, nil)
	bad := machine("bad", 1, node, api.RunAlways)
	bad.Spec.Image = "registry.example/app:latest"
	e.gaon.SetMachine(bad)
	e.gaon.SetMachine(machine("good", 1, node, api.RunAlways))

	eventually(t, "status Failed", e.phase("bad", 1, api.MachineFailed))
	eventually(t, "control: a Machine with a digest runs", e.running("good-e1"))
	assert.Equal(t, machines.ReasonInvalidSpec, e.gaon.Machine("bad").Status.Conditions[0].Reason)
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine bad-e1")
}

func TestRun_KeepsAMachinePendingUntilMvmdsDeadlineIsArmed(t *testing.T) {
	e := start(t, false, nil)
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))

	eventually(t, "created and Pending", func() bool {
		return e.runtimeState("m1-e1") == v1alpha2.MachineState_MACHINE_STATE_CREATED
	})
	assert.NotEqual(t, api.MachineRunning, e.gaon.Machine("m1").Status.Phase)

	e.mvmd.Arm(time.Hour, 2)

	eventually(t, "m1-e1 runs once armed", e.running("m1-e1"))
	eventually(t, "status Running", e.phase("m1", 1, api.MachineRunning))
}

func TestRun_RemovesWhatItCreatedBeforeARestartAndLeavesWhatItDidNot(t *testing.T) {
	e := start(t, true, nil, func(e *env) {
		for _, id := range []string{"orphan-e3", "foreign"} {
			_, err := e.mvmd.CreateMachine(context.Background(), &v1alpha2.CreateMachineRequest{MachineId: id})
			require.NoError(t, err)
		}
	})
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))

	eventually(t, "the orphan of an earlier run is removed", e.gone("orphan-e3"))
	eventually(t, "m1 runs", e.running("m1-e1"))
	assert.Contains(t, e.mvmd.IDs(), "foreign", "an mvmd machine the agent did not name is not touched")
}

// conflictOnce answers the first status write with a conflict, as a write
// does when the Machine changed since it was read.
type conflictOnce struct {
	machines.API
	hit atomic.Bool
}

func (c *conflictOnce) PutMachineStatus(ctx context.Context, m *api.Machine) (*api.Machine, error) {
	if !c.hit.Swap(true) {
		return nil, api.NewAPIError(api.ReasonConflict, "version is stale")
	}
	return c.API.PutMachineStatus(ctx, m)
}

func TestRun_ReadsTheMachineAgainAndRetriesAStatusWriteThatConflicts(t *testing.T) {
	c := &conflictOnce{}
	e := start(t, true, func(a machines.API) machines.API { c.API = a; return c })
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))

	eventually(t, "status Running", e.phase("m1", 1, api.MachineRunning))
	assert.True(t, c.hit.Load(), "setup: the first write was answered with a conflict")
}

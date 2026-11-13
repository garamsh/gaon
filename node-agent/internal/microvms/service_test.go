package microvms_test

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
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/microvms"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const node = "n1"

var image = "registry.example/app@sha256:" + strings.Repeat("a", 64)

type env struct {
	gaon *fakegaon.Server
	mvmd *fakemvmd.Server
	rt   v1alpha2.RuntimeServiceClient
	svc  microvms.Service
	// network and storage are the plugins the service runs with; a setup
	// function may replace them, and they default to plugins that serve every
	// MicroVM with the empty policy and no disk.
	network microvms.Network
	storage microvms.Storage
	// log receives the service's log; a setup function may replace it, and it
	// defaults to a writer that discards.
	log io.Writer
}

// start runs the service against fakes, with the fencing deadline armed
// unless armed is false, and stops it when the test ends.
func start(t *testing.T, armed bool, wrap func(microvms.API) microvms.API, setup ...func(*env)) *env {
	t.Helper()
	clk := clock.System{}
	e := &env{gaon: fakegaon.New(t), mvmd: fakemvmd.New(t, clk)}
	for _, f := range setup {
		f(e)
	}
	if e.network == nil {
		e.network = emptyNetwork{}
	}
	if e.storage == nil {
		e.storage = noDisks{}
	}
	if e.log == nil {
		e.log = io.Discard
	}
	conn, err := runtimeapi.Dial(context.Background(), e.mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	e.rt = v1alpha2.NewRuntimeServiceClient(conn)
	if armed {
		e.mvmd.Arm(time.Hour, 1)
	}
	var client microvms.API = api.NewClient(e.gaon.URL, nil)
	if wrap != nil {
		client = wrap(client)
	}
	svc := microvms.NewService(node, microvms.Params{
		Resync: 20 * time.Millisecond, Retry: 20 * time.Millisecond, ReadTimeout: time.Second,
		RestartBackoff: time.Hour, RestartBackoffMax: time.Hour, RestartResetAfter: time.Hour,
	},
		client, e.rt, e.network, e.storage, clk, slog.New(slog.NewTextHandler(e.log, nil)))
	e.svc = svc
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return e
}

// emptyNetwork gives every MicroVM the empty policy.
type emptyNetwork struct{}

func (emptyNetwork) Policy(context.Context, *api.MicroVM) (*plugin.NetworkPolicy, error) {
	return &plugin.NetworkPolicy{}, nil
}

// noDisks attaches nothing and releases nothing.
type noDisks struct{}

func (noDisks) Attach(context.Context, *api.MicroVM) ([]plugin.DiskAttachment, error) {
	return nil, nil
}

func (noDisks) Release(context.Context, string) error { return nil }

func microvm(name string, epoch api.Epoch, nodeName string, strategy api.RunStrategy) api.MicroVM {
	return api.MicroVM{
		Metadata: api.ObjectMeta{Name: name, Generation: 1},
		Spec: api.MicroVMSpec{
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

func (e *env) phase(name string, epoch api.Epoch, phase api.MicroVMPhase) func() bool {
	return func() bool {
		s := e.gaon.MicroVM(name).Status
		return s.BindingEpoch == epoch && s.Phase == phase
	}
}

func (e *env) gone(id string) func() bool {
	return func() bool { return !slices.Contains(e.mvmd.IDs(), id) }
}

func TestRun_CreatesAndStartsAMicroVMBoundToThisNodeAtItsEpoch(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))

	eventually(t, "mvmd runs m1-e1", e.running("m1-e1"))
	eventually(t, "status Running at epoch 1", e.phase("m1", 1, api.MicroVMRunning))

	got, _ := e.mvmd.Machine("m1-e1")
	assert.EqualValues(t, 2, got.GetResources().GetCpuCount())
	assert.EqualValues(t, 1<<30, got.GetResources().GetMemoryBytes())
	assert.Equal(t, []string{strings.Repeat("a", 64)}, trimDigests(e.mvmd.Images()))
	s := e.gaon.MicroVM("m1").Status
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

func TestRun_IgnoresAMicroVMBoundToAnotherNode(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("other", 1, "n2", api.RunAlways))
	e.gaon.SetMicroVM(microvm("mine", 1, node, api.RunAlways))

	eventually(t, "control: mine runs", e.running("mine-e1"))
	assert.NotContains(t, e.mvmd.IDs(), "other-e1")
	assert.Empty(t, e.gaon.MicroVM("other").Status.Phase, "the agent writes no status of a MicroVM that is not bound to it")
}

func TestRun_StopsAndRemovesAMicroVMReboundElsewhere(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("keep", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))
	eventually(t, "m1 runs", e.running("m1-e1"))

	e.gaon.SetMicroVM(microvm("m1", 2, "n2", api.RunAlways))

	eventually(t, "m1-e1 is removed from mvmd", e.gone("m1-e1"))
	assert.Contains(t, e.mvmd.Calls(), "StopMachine m1-e1", "it is stopped before it is deleted")
	assert.Equal(t, v1alpha2.MachineState_MACHINE_STATE_RUNNING, e.runtimeState("keep-e1"), "control: a MicroVM still bound here is left running")
}

func TestRun_StopsAndRemovesAMicroVMUnboundOrDeleted(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("unbound", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("deleted", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("keep", 1, node, api.RunAlways))
	eventually(t, "all run", func() bool {
		return e.running("unbound-e1")() && e.running("deleted-e1")() && e.running("keep-e1")()
	})

	e.gaon.SetMicroVM(microvm("unbound", 1, "", api.RunAlways))
	e.gaon.DeleteMicroVM("deleted")

	eventually(t, "unbound-e1 is removed", e.gone("unbound-e1"))
	eventually(t, "deleted-e1 is removed", e.gone("deleted-e1"))
	assert.True(t, e.running("keep-e1")(), "control: the MicroVM still bound here runs")
}

func TestRun_ReplacesTheMicroVMWhenReboundToThisNodeAtALaterEpoch(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs", e.running("m1-e1"))

	e.gaon.SetMicroVM(microvm("m1", 2, node, api.RunAlways))

	eventually(t, "m1-e2 runs", e.running("m1-e2"))
	eventually(t, "m1-e1 is removed", e.gone("m1-e1"))
	eventually(t, "status is that of epoch 2", e.phase("m1", 2, api.MicroVMRunning))
}

func TestRun_IgnoresABindingOlderThanTheNewestEpochSeen(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("m1", 2, node, api.RunAlways))
	eventually(t, "m1-e2 runs", e.running("m1-e2"))

	// A delayed event shows the MicroVM bound here at epoch 1, then a later
	// MicroVM proves the stream was read past it.
	e.gaon.Inject(api.EventModified, microvm("m1", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("marker", 1, node, api.RunAlways))
	eventually(t, "marker runs, so the stale event has been read", e.running("marker-e1"))

	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine m1-e1")
	assert.True(t, e.running("m1-e2")(), "the newest binding is left running")
	assert.EqualValues(t, 2, e.gaon.MicroVM("m1").Spec.BindingEpoch)
}

func TestRun_ReportsPhaseFromMvmdState(t *testing.T) {
	tests := []struct {
		name     string
		strategy api.RunStrategy
		state    v1alpha2.MachineState
		reason   string
		phase    api.MicroVMPhase
		cond     string
		message  string
	}{
		{"a faulted VMM is Failed with mvmd's reason", api.RunAlways, v1alpha2.MachineState_MACHINE_STATE_FAULTED, "vmm exited: oom", api.MicroVMFailed, microvms.ReasonFaulted, "vmm exited: oom"},
		{"a paused MicroVM is Suspended", api.RunAlways, v1alpha2.MachineState_MACHINE_STATE_PAUSED, "", api.MicroVMSuspended, microvms.ReasonSuspended, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := start(t, true, nil)
			e.gaon.SetMicroVM(microvm("m1", 1, node, tt.strategy))
			eventually(t, "status Running", e.phase("m1", 1, api.MicroVMRunning))

			e.mvmd.SetState("m1-e1", tt.state, tt.reason)

			eventually(t, "phase "+string(tt.phase), e.phase("m1", 1, tt.phase))
			s := e.gaon.MicroVM("m1").Status
			require.Len(t, s.Conditions, 1)
			assert.Equal(t, microvms.ConditionReady, s.Conditions[0].Type)
			assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
			assert.Equal(t, tt.cond, s.Conditions[0].Reason)
			assert.Equal(t, tt.message, s.Conditions[0].Message)
		})
	}
}

func TestRun_StopsAHaltedMicroVMAndStartsItAgainWhenResumed(t *testing.T) {
	e := start(t, true, nil)
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs", e.running("m1-e1"))

	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunHalted))
	eventually(t, "status Stopped", e.phase("m1", 1, api.MicroVMStopped))
	assert.Equal(t, v1alpha2.MachineState_MACHINE_STATE_STOPPED, e.runtimeState("m1-e1"))

	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs again", e.running("m1-e1"))
	eventually(t, "status Running", e.phase("m1", 1, api.MicroVMRunning))
}

func TestRun_ReportsFailedWhenMvmdRejectsTheCreateAboveMaxVCPUs(t *testing.T) {
	e := start(t, true, nil, func(e *env) { e.mvmd.MaxVCPUs = 2 })
	over := microvm("over", 1, node, api.RunAlways)
	over.Spec.Resources.Limits.CPU = "4"
	e.gaon.SetMicroVM(over)
	e.gaon.SetMicroVM(microvm("within", 1, node, api.RunAlways))

	eventually(t, "status Failed", e.phase("over", 1, api.MicroVMFailed))
	eventually(t, "control: a MicroVM at the limit runs", e.running("within-e1"))

	s := e.gaon.MicroVM("over").Status
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, microvms.ReasonRejected, s.Conditions[0].Reason)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	assert.Contains(t, s.Conditions[0].Message, "max_vcpus", "the condition carries mvmd's reason")
	assert.NotContains(t, e.mvmd.IDs(), "over-e1")
}

func TestRun_CreatesAMicroVMWithTheKernelItsSpecNames(t *testing.T) {
	e := start(t, true, nil, func(e *env) { e.mvmd.Kernels = []string{"default", "alt"} })
	named := microvm("named", 1, node, api.RunAlways)
	named.Spec.Kernel = "alt"
	e.gaon.SetMicroVM(named)
	e.gaon.SetMicroVM(microvm("unnamed", 1, node, api.RunAlways))

	eventually(t, "named-e1 runs", e.running("named-e1"))
	eventually(t, "control: unnamed-e1 runs", e.running("unnamed-e1"))
	got, _ := e.mvmd.Machine("named-e1")
	assert.Equal(t, "alt", got.GetKernel())
	other, _ := e.mvmd.Machine("unnamed-e1")
	assert.Empty(t, other.GetKernel(), "an empty spec.kernel stays empty for mvmd's default")
}

func TestRun_ReportsFailedWhenMvmdRejectsTheCreateForAnUnknownKernel(t *testing.T) {
	e := start(t, true, nil, func(e *env) { e.mvmd.Kernels = []string{"default"} })
	bad := microvm("bad", 1, node, api.RunAlways)
	bad.Spec.Kernel = "missing"
	e.gaon.SetMicroVM(bad)
	good := microvm("good", 1, node, api.RunAlways)
	good.Spec.Kernel = "default"
	e.gaon.SetMicroVM(good)

	eventually(t, "status Failed", e.phase("bad", 1, api.MicroVMFailed))
	eventually(t, "control: a MicroVM naming an offered kernel runs", e.running("good-e1"))
	s := e.gaon.MicroVM("bad").Status
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, microvms.ReasonRejected, s.Conditions[0].Reason)
	assert.Contains(t, s.Conditions[0].Message, "missing")
	assert.NotContains(t, e.mvmd.IDs(), "bad-e1")
}

func TestRun_ReportsFailedForAnImageWithoutADigest(t *testing.T) {
	e := start(t, true, nil)
	bad := microvm("bad", 1, node, api.RunAlways)
	bad.Spec.Image = "registry.example/app:latest"
	e.gaon.SetMicroVM(bad)
	e.gaon.SetMicroVM(microvm("good", 1, node, api.RunAlways))

	eventually(t, "status Failed", e.phase("bad", 1, api.MicroVMFailed))
	eventually(t, "control: a MicroVM with a digest runs", e.running("good-e1"))
	assert.Equal(t, microvms.ReasonInvalidSpec, e.gaon.MicroVM("bad").Status.Conditions[0].Reason)
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine bad-e1")
}

func TestRun_KeepsAMicroVMPendingUntilMvmdsDeadlineIsArmed(t *testing.T) {
	e := start(t, false, nil)
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))

	eventually(t, "created and Pending", func() bool {
		return e.runtimeState("m1-e1") == v1alpha2.MachineState_MACHINE_STATE_CREATED
	})
	assert.NotEqual(t, api.MicroVMRunning, e.gaon.MicroVM("m1").Status.Phase)

	e.mvmd.Arm(time.Hour, 2)

	eventually(t, "m1-e1 runs once armed", e.running("m1-e1"))
	eventually(t, "status Running", e.phase("m1", 1, api.MicroVMRunning))
}

func TestRun_RemovesWhatItCreatedBeforeARestartAndLeavesWhatItDidNot(t *testing.T) {
	e := start(t, true, nil, func(e *env) {
		for _, id := range []string{"orphan-e3", "foreign"} {
			_, err := e.mvmd.CreateMachine(context.Background(), &v1alpha2.CreateMachineRequest{MachineId: id})
			require.NoError(t, err)
		}
	})
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))

	eventually(t, "the orphan of an earlier run is removed", e.gone("orphan-e3"))
	eventually(t, "m1 runs", e.running("m1-e1"))
	assert.Contains(t, e.mvmd.IDs(), "foreign", "an mvmd machine the agent did not name is not touched")
}

// conflictOnce answers the first status write with a conflict, as a write
// does when the MicroVM changed since it was read.
type conflictOnce struct {
	microvms.API
	hit atomic.Bool
}

func (c *conflictOnce) PutMicroVMStatus(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error) {
	if !c.hit.Swap(true) {
		return nil, api.NewAPIError(api.ReasonConflict, "version is stale")
	}
	return c.API.PutMicroVMStatus(ctx, m)
}

func TestRun_ReadsTheMicroVMAgainAndRetriesAStatusWriteThatConflicts(t *testing.T) {
	c := &conflictOnce{}
	e := start(t, true, func(a microvms.API) microvms.API { c.API = a; return c })
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))

	eventually(t, "status Running", e.phase("m1", 1, api.MicroVMRunning))
	assert.True(t, c.hit.Load(), "setup: the first write was answered with a conflict")
}

package machines

// These tests are internal because they drive reconcileAll round by round on a
// fake clock that only moves when told to: the restart waits are exact only
// when no loop of the service advances time on its own.

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	rigNode = "n1"
	rigID   = "m1-e1"
)

var rigImage = "registry.example/app@sha256:" + strings.Repeat("a", 64)

type rig struct {
	t    *testing.T
	gaon *fakegaon.Server
	mvmd *fakemvmd.Server
	clk  *clock.Fake
	svc  *service
}

// newRig returns a service over fakes on a fake clock, with the backoff at its
// recorded defaults and the fencing deadline armed far beyond any time a test
// advances.
func newRig(t *testing.T, wrap func(API) API) *rig {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	r := &rig{t: t, gaon: fakegaon.New(t), mvmd: fakemvmd.New(t, clk), clk: clk}
	conn, err := runtimeapi.Dial(context.Background(), r.mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	r.mvmd.Arm(100000*time.Hour, 1)
	var client API = api.NewClient(r.gaon.URL, nil)
	if wrap != nil {
		client = wrap(client)
	}
	params := Params{
		RestartBackoff: 10 * time.Second, RestartBackoffMax: 5 * time.Minute, RestartResetAfter: 10 * time.Minute,
		ReadTimeout: 200 * time.Millisecond,
	}
	r.svc = NewService(rigNode, params, client, v1alpha2.NewRuntimeServiceClient(conn), emptyNetwork{}, noStorage{}, clk, slog.New(slog.NewTextHandler(io.Discard, nil))).(*service)
	return r
}

// emptyNetwork and noStorage are plugins that add nothing, so the rig tests
// restarts alone; this package cannot import the built-ins, which import it.
type emptyNetwork struct{}

func (emptyNetwork) Policy(context.Context, *api.Machine) (*v1alpha2.NetworkPolicy, error) {
	return &v1alpha2.NetworkPolicy{}, nil
}

type noStorage struct{}

func (noStorage) Attach(context.Context, *api.Machine) ([]*v1alpha2.DiskAttachment, error) {
	return nil, nil
}
func (noStorage) Release(context.Context, string) error { return nil }

func rigMachine(epoch api.Epoch, strategy api.RunStrategy) api.Machine {
	return api.Machine{
		Metadata: api.ObjectMeta{Name: "m1", Generation: 1},
		Spec: api.MachineSpec{
			Image:        rigImage,
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "1", Memory: "1Gi"}},
			RunStrategy:  strategy,
			NodeName:     rigNode,
			BindingEpoch: epoch,
		},
	}
}

// put stores m as a user or the Scheduler does and gives it to the service.
func (r *rig) put(m api.Machine) {
	r.t.Helper()
	r.gaon.SetMachine(m)
	list, err := r.svc.api.ListMachines(context.Background())
	require.NoError(r.t, err)
	r.svc.cache.replace(list.Items)
}

// round runs one reconciliation and returns its error.
func (r *rig) round() error {
	return r.svc.reconcileAll(context.Background())
}

func (r *rig) mustRound() {
	r.t.Helper()
	require.NoError(r.t, r.round())
}

// begin stores a Machine under strategy and runs it until mvmd holds it
// Running.
func (r *rig) begin(strategy api.RunStrategy) {
	r.t.Helper()
	r.put(rigMachine(1, strategy))
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
}

func (r *rig) requireState(want v1alpha2.MachineState) {
	r.t.Helper()
	got, ok := r.mvmd.Machine(rigID)
	require.True(r.t, ok)
	require.Equal(r.t, want, got.GetState())
}

func (r *rig) starts() int {
	n := 0
	for _, c := range r.mvmd.Calls() {
		if c == "StartMachine "+rigID {
			n++
		}
	}
	return n
}

// exit ends the entrypoint with rec and stops the Machine, as mvmd does.
func (r *rig) exit(rec *v1alpha2.ExecRecord) {
	r.mvmd.SetEntrypointRecord(rigID, rec)
	r.mvmd.SetState(rigID, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "the VMM ended with no call asking it to")
}

func exited(code, signal int32) *v1alpha2.ExecRecord {
	return &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED, ExitCode: code, TermSignal: signal}
}

// restartAfter ends the run with a failure and checks that the Machine stays
// stopped until wait has passed since the exit was seen, and runs once it has.
func (r *rig) restartAfter(wait time.Duration) {
	r.t.Helper()
	r.exit(exited(1, 0))
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
	r.clk.Advance(wait - time.Second)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
	r.clk.Advance(time.Second)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
}

func TestRestart_StartsAgainAfterTheBackoffAndNotBefore(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)

	r.restartAfter(10 * time.Second)

	assert.Equal(t, 2, r.starts())
}

func TestRestart_DoublesTheWaitWithEachRestart(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)

	for _, wait := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second} {
		r.restartAfter(wait)
	}
}

func TestRestart_CapsTheWaitAtTheMaximum(t *testing.T) {
	r := newRig(t, nil)
	r.svc.params.RestartBackoffMax = 25 * time.Second
	r.begin(api.RunAlways)

	for _, wait := range []time.Duration{10 * time.Second, 20 * time.Second, 25 * time.Second, 25 * time.Second} {
		r.restartAfter(wait)
	}
}

func TestRestart_StartsTheWaitOverAfterTheMachineRanForTheResetTime(t *testing.T) {
	tests := []struct {
		name string
		ran  time.Duration
		wait time.Duration
	}{
		{"it ran for the reset time", 10 * time.Minute, 10 * time.Second},
		{"it ran a second short of it", 10*time.Minute - time.Second, 40 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.begin(api.RunAlways)
			r.restartAfter(10 * time.Second)
			r.restartAfter(20 * time.Second)

			r.clk.Advance(tt.ran)
			r.mustRound()
			r.restartAfter(tt.wait)
		})
	}
}

func TestRestart_RerunOnFailureRestartsAFailureAndNotACleanExit(t *testing.T) {
	failed := func(r *rig) {}
	tests := []struct {
		name    string
		prepare func(*rig)
		record  *v1alpha2.ExecRecord
		state   v1alpha2.MachineState
		restart bool
	}{
		{"a non-zero exit code", failed, exited(1, 0), v1alpha2.MachineState_MACHINE_STATE_STOPPED, true},
		{"a signal with code zero", failed, exited(0, 9), v1alpha2.MachineState_MACHINE_STATE_STOPPED, true},
		{"a clean exit", failed, exited(0, 0), v1alpha2.MachineState_MACHINE_STATE_STOPPED, false},
		{"a record that says cut short", failed, &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: v1alpha2.ExecStatus_EXEC_STATUS_CUT_SHORT}, v1alpha2.MachineState_MACHINE_STATE_STOPPED, true},
		{"a record that says not started", failed, &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: v1alpha2.ExecStatus_EXEC_STATUS_NOT_STARTED}, v1alpha2.MachineState_MACHINE_STATE_STOPPED, true},
		{"a record that says started", failed, &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: v1alpha2.ExecStatus_EXEC_STATUS_STARTED}, v1alpha2.MachineState_MACHINE_STATE_STOPPED, true},
		{"a read that ends with no record", func(r *rig) { r.mvmd.SilenceEntrypoint(rigID, true) }, nil, v1alpha2.MachineState_MACHINE_STATE_STOPPED, true},
		{"a faulted Machine", failed, nil, v1alpha2.MachineState_MACHINE_STATE_FAULTED, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.begin(api.RunRerunOnFailure)
			tt.prepare(r)
			if tt.record != nil {
				r.mvmd.SetEntrypointRecord(rigID, tt.record)
			}
			r.mvmd.SetState(rigID, tt.state, "gone")

			r.mustRound()
			r.clk.Advance(time.Hour)
			r.mustRound()

			if tt.restart {
				r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
				assert.Equal(t, 2, r.starts())
				return
			}
			r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
			assert.Equal(t, 1, r.starts(), "a clean exit is not started again, however long the wait")
			s := r.gaon.Machine("m1").Status
			assert.Equal(t, api.MachineSucceeded, s.Phase)
			require.Len(t, s.Conditions, 1)
			assert.Contains(t, s.Conditions[0].Message, "cleanly")
		})
	}
}

func TestRestart_AlwaysStartsAgainAfterACleanExitAndAfterAFailure(t *testing.T) {
	for name, rec := range map[string]*v1alpha2.ExecRecord{"a clean exit": exited(0, 0), "a failure": exited(2, 0)} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.begin(api.RunAlways)
			r.exit(rec)

			r.mustRound()
			r.clk.Advance(10 * time.Second)
			r.mustRound()

			r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
		})
	}
}

func TestRestart_DoesNotTrustACleanRecordEqualToTheOneReadAtTheExitBefore(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)
	r.exit(exited(0, 0))
	r.mustRound()
	r.clk.Advance(10 * time.Second)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)

	m := r.gaon.Machine("m1")
	m.Spec.RunStrategy = api.RunRerunOnFailure
	r.put(m)
	// The Machine stops again and the record is the one the last run left.
	r.mvmd.SetState(rigID, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "the VMM ended with no call asking it to")
	r.mustRound()
	r.clk.Advance(time.Hour)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	assert.Equal(t, 3, r.starts(), "a clean record that may be the earlier run's does not stop the restart")
}

func TestRestart_AcceptsACleanRecordThatDiffersFromTheExitBefore(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunRerunOnFailure)
	r.restartAfter(10 * time.Second)

	r.exit(exited(0, 0))
	r.mustRound()
	r.clk.Advance(time.Hour)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
	assert.Equal(t, 2, r.starts())
}

func TestRestart_StopsAFaultedMachineBeforeStartingIt(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)
	r.mvmd.SetState(rigID, v1alpha2.MachineState_MACHINE_STATE_FAULTED, "stop not confirmed")

	r.mustRound()
	assert.Equal(t, api.MachineFailed, r.gaon.Machine("m1").Status.Phase, "while it waits, the Machine is Failed")
	r.clk.Advance(10 * time.Second)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	calls := r.mvmd.Calls()
	assert.Equal(t, []string{"StopMachine " + rigID, "StartMachine " + rigID}, calls[len(calls)-2:])
}

func TestRestart_WritesRestartCountAndLeavesLastExitEmpty(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)
	assert.EqualValues(t, 0, r.gaon.Machine("m1").Status.RestartCount)

	r.restartAfter(10 * time.Second)
	assert.EqualValues(t, 1, r.gaon.Machine("m1").Status.RestartCount)
	r.restartAfter(20 * time.Second)

	s := r.gaon.Machine("m1").Status
	assert.EqualValues(t, 2, s.RestartCount)
	assert.Nil(t, s.LastExit, "mvmd reports no exit time, so lastExit is not written")
	assert.EqualValues(t, 1, s.BindingEpoch)
}

// conflictOnStatus answers the first status write with a conflict, as a write
// does when the Machine changed since it was read.
type conflictOnStatus struct {
	API
	hit atomic.Bool
}

func (c *conflictOnStatus) PutMachineStatus(ctx context.Context, m *api.Machine) (*api.Machine, error) {
	if m.Status.RestartCount > 0 && !c.hit.Swap(true) {
		return nil, api.NewAPIError(api.ReasonConflict, "version is stale")
	}
	return c.API.PutMachineStatus(ctx, m)
}

func TestRestart_RestartCountIsWrittenByCompareAndSwapAndCountedOnce(t *testing.T) {
	c := &conflictOnStatus{}
	r := newRig(t, func(a API) API { c.API = a; return c })
	r.begin(api.RunAlways)

	r.restartAfter(10 * time.Second)

	assert.True(t, c.hit.Load(), "setup: the write of the count was answered with a conflict")
	assert.EqualValues(t, 1, r.gaon.Machine("m1").Status.RestartCount, "the write is read again and retried, and the restart counted once")
}

// failStatusOnce fails the first status write that carries a restart with a
// server error.
type failStatusOnce struct {
	API
	hit atomic.Bool
}

func (c *failStatusOnce) PutMachineStatus(ctx context.Context, m *api.Machine) (*api.Machine, error) {
	if m.Status.RestartCount > 0 && !c.hit.Swap(true) {
		return nil, errors.New("store is down")
	}
	return c.API.PutMachineStatus(ctx, m)
}

func TestRestart_WritesARestartAgainWhenTheWriteFailed(t *testing.T) {
	c := &failStatusOnce{}
	r := newRig(t, func(a API) API { c.API = a; return c })
	r.begin(api.RunAlways)
	r.exit(exited(1, 0))
	r.mustRound()
	r.clk.Advance(10 * time.Second)

	require.Error(t, r.round(), "the restart happened and its write failed")
	assert.EqualValues(t, 0, r.gaon.Machine("m1").Status.RestartCount)
	r.mustRound()

	assert.EqualValues(t, 1, r.gaon.Machine("m1").Status.RestartCount)
	assert.Equal(t, 2, r.starts(), "the next round does not start it again")
}

func TestRestart_CountsFromZeroAtANewEpoch(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)
	r.restartAfter(10 * time.Second)
	r.restartAfter(20 * time.Second)
	require.EqualValues(t, 2, r.gaon.Machine("m1").Status.RestartCount)

	// The Scheduler's binding changes the spec and leaves the status as it was.
	m := r.gaon.Machine("m1")
	m.Spec.BindingEpoch = 2
	r.put(m)
	r.mustRound()

	s := r.gaon.Machine("m1").Status
	assert.EqualValues(t, 2, s.BindingEpoch)
	assert.EqualValues(t, 0, s.RestartCount, "the restarts of epoch 1 are not carried into epoch 2")
	assert.Equal(t, api.MachineRunning, s.Phase)
}

func TestRestart_StartsAHaltedMachineAgainAtOnceWhenResumedAndDoesNotCountIt(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunAlways)
	m := r.gaon.Machine("m1")
	m.Spec.RunStrategy = api.RunHalted
	r.put(m)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)

	m = r.gaon.Machine("m1")
	m.Spec.RunStrategy = api.RunAlways
	r.put(m)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	assert.EqualValues(t, 0, r.gaon.Machine("m1").Status.RestartCount)
}

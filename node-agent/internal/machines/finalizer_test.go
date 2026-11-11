package machines_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/machines"
)

const otherFinalizer = "example.com/other"

// putHook runs before each spec write and may refuse it.
type putHook struct {
	machines.API
	before func(m *api.Machine) error
	puts   atomic.Int32
}

func (h *putHook) PutMachine(ctx context.Context, m *api.Machine) (*api.Machine, error) {
	h.puts.Add(1)
	if err := h.before(m); err != nil {
		return nil, err
	}
	return h.API.PutMachine(ctx, m)
}

func (e *env) finalizers(name string) []string { return e.gaon.Machine(name).Metadata.Finalizers }

func TestRun_AddsItsFinalizerBeforeStartingAMachineAndChangesNothingElse(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	h := &putHook{before: func(*api.Machine) error {
		if refuse.Load() {
			return api.NewAPIError(api.ReasonInvalid, "refused")
		}
		return nil
	}}
	e := start(t, true, func(a machines.API) machines.API { h.API = a; return h })
	m := machine("m1", 1, node, api.RunAlways)
	m.Metadata.Finalizers = []string{otherFinalizer}
	e.gaon.SetMachine(m)

	eventually(t, "the agent tried to add its finalizer", func() bool { return h.puts.Load() >= 2 })
	assert.Empty(t, e.mvmd.IDs(), "no Machine is created while the finalizer is not written")
	assert.NotContains(t, e.mvmd.Calls(), "StartMachine m1-e1")

	refuse.Store(false)
	eventually(t, "m1-e1 runs", e.running("m1-e1"))
	got := e.gaon.Machine("m1")
	assert.Equal(t, []string{otherFinalizer, machines.Finalizer}, got.Metadata.Finalizers, "its own entry is added and the other kept")
	assert.Equal(t, m.Spec, got.Spec, "the write changes nothing but the finalizers")
}

func TestRun_ReadsAgainAndRetriesTheFinalizerWriteThatConflicts(t *testing.T) {
	var gaon *fakegaon.Server
	var conflicted atomic.Bool
	h := &putHook{before: func(m *api.Machine) error {
		if !conflicted.Swap(true) {
			gaon.BumpVersion(m.Metadata.Name)
		}
		return nil
	}}
	e := start(t, true, func(a machines.API) machines.API { h.API = a; return h }, func(e *env) { gaon = e.gaon })
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))

	eventually(t, "m1-e1 runs", e.running("m1-e1"))
	assert.Equal(t, []string{machines.Finalizer}, e.finalizers("m1"))
	assert.GreaterOrEqual(t, h.puts.Load(), int32(2), "the first write conflicted and a second was made")
}

func TestRun_RemovesOnlyItsOwnFinalizerOnceAMachineBeingDeletedIsReleased(t *testing.T) {
	e := start(t, true, nil)
	m := machine("m1", 1, node, api.RunAlways)
	m.Metadata.Finalizers = []string{otherFinalizer}
	e.gaon.SetMachine(m)
	e.gaon.SetMachine(machine("solo", 1, node, api.RunAlways))
	eventually(t, "both run", func() bool { return e.running("m1-e1")() && e.running("solo-e1")() })
	require.Equal(t, []string{otherFinalizer, machines.Finalizer}, e.finalizers("m1"))

	e.gaon.MarkDeleting("m1")
	e.gaon.MarkDeleting("solo")

	eventually(t, "m1-e1 is released on mvmd", e.gone("m1-e1"))
	eventually(t, "solo is removed by the write clearing its last finalizer", func() bool {
		return e.gaon.Machine("solo").Metadata.Name == ""
	})
	eventually(t, "the agent's entry is gone from m1", func() bool { return len(e.finalizers("m1")) == 1 })
	assert.Equal(t, []string{otherFinalizer}, e.finalizers("m1"), "the other finalizer stays")
	assert.NotNil(t, e.gaon.Machine("m1").Metadata.DeletionTimestamp)
}

func TestRun_KeepsItsFinalizerWhileMvmdStillHoldsTheMachine(t *testing.T) {
	h := &putHook{before: func(*api.Machine) error { return nil }}
	e := start(t, true, func(a machines.API) machines.API { h.API = a; return h })
	e.gaon.SetMachine(machine("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs", e.running("m1-e1"))
	e.mvmd.FailDeletes(true)

	e.gaon.MarkDeleting("m1")

	eventually(t, "the agent tried to delete", func() bool { return slices.Contains(e.mvmd.Calls(), "DeleteMachine m1-e1") })
	before := h.puts.Load()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, []string{machines.Finalizer}, e.finalizers("m1"), "the finalizer stays while mvmd holds the Machine")
	assert.Equal(t, before, h.puts.Load(), "no write of the finalizers is attempted")

	e.mvmd.FailDeletes(false)
	eventually(t, "released, the Machine is removed", func() bool { return e.gaon.Machine("m1").Metadata.Name == "" })
}

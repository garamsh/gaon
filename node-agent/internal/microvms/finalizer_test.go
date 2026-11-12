package microvms_test

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
	"github.com/garamsh/gaon/node-agent/internal/microvms"
)

const otherFinalizer = "example.com/other"

// putHook runs before each spec write and may refuse it.
type putHook struct {
	microvms.API
	before func(m *api.MicroVM) error
	puts   atomic.Int32
}

func (h *putHook) PutMicroVM(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error) {
	h.puts.Add(1)
	if err := h.before(m); err != nil {
		return nil, err
	}
	return h.API.PutMicroVM(ctx, m)
}

func (e *env) finalizers(name string) []string { return e.gaon.MicroVM(name).Metadata.Finalizers }

func TestRun_AddsItsFinalizerBeforeStartingAMicroVMAndChangesNothingElse(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	h := &putHook{before: func(*api.MicroVM) error {
		if refuse.Load() {
			return api.NewAPIError(api.ReasonInvalid, "refused")
		}
		return nil
	}}
	e := start(t, true, func(a microvms.API) microvms.API { h.API = a; return h })
	m := microvm("m1", 1, node, api.RunAlways)
	m.Metadata.Finalizers = []string{otherFinalizer}
	e.gaon.SetMicroVM(m)

	eventually(t, "the agent tried to add its finalizer", func() bool { return h.puts.Load() >= 2 })
	assert.Empty(t, e.mvmd.IDs(), "no MicroVM is created while the finalizer is not written")
	assert.NotContains(t, e.mvmd.Calls(), "StartMachine m1-e1")

	refuse.Store(false)
	eventually(t, "m1-e1 runs", e.running("m1-e1"))
	got := e.gaon.MicroVM("m1")
	assert.Equal(t, []string{otherFinalizer, microvms.Finalizer}, got.Metadata.Finalizers, "its own entry is added and the other kept")
	assert.Equal(t, m.Spec, got.Spec, "the write changes nothing but the finalizers")
}

func TestRun_ReadsAgainAndRetriesTheFinalizerWriteThatConflicts(t *testing.T) {
	var gaon *fakegaon.Server
	var conflicted atomic.Bool
	h := &putHook{before: func(m *api.MicroVM) error {
		if !conflicted.Swap(true) {
			gaon.BumpVersion(m.Metadata.Name)
		}
		return nil
	}}
	e := start(t, true, func(a microvms.API) microvms.API { h.API = a; return h }, func(e *env) { gaon = e.gaon })
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))

	eventually(t, "m1-e1 runs", e.running("m1-e1"))
	assert.Equal(t, []string{microvms.Finalizer}, e.finalizers("m1"))
	assert.GreaterOrEqual(t, h.puts.Load(), int32(2), "the first write conflicted and a second was made")
}

func TestRun_RemovesOnlyItsOwnFinalizerOnceAMicroVMBeingDeletedIsReleased(t *testing.T) {
	e := start(t, true, nil)
	m := microvm("m1", 1, node, api.RunAlways)
	m.Metadata.Finalizers = []string{otherFinalizer}
	e.gaon.SetMicroVM(m)
	e.gaon.SetMicroVM(microvm("solo", 1, node, api.RunAlways))
	eventually(t, "both run", func() bool { return e.running("m1-e1")() && e.running("solo-e1")() })
	require.Equal(t, []string{otherFinalizer, microvms.Finalizer}, e.finalizers("m1"))

	e.gaon.MarkDeleting("m1")
	e.gaon.MarkDeleting("solo")

	eventually(t, "m1-e1 is released on mvmd", e.gone("m1-e1"))
	eventually(t, "solo is removed by the write clearing its last finalizer", func() bool {
		return e.gaon.MicroVM("solo").Metadata.Name == ""
	})
	eventually(t, "the agent's entry is gone from m1", func() bool { return len(e.finalizers("m1")) == 1 })
	assert.Equal(t, []string{otherFinalizer}, e.finalizers("m1"), "the other finalizer stays")
	assert.NotNil(t, e.gaon.MicroVM("m1").Metadata.DeletionTimestamp)
}

func TestRun_KeepsItsFinalizerWhileMvmdStillHoldsTheMicroVM(t *testing.T) {
	h := &putHook{before: func(*api.MicroVM) error { return nil }}
	e := start(t, true, func(a microvms.API) microvms.API { h.API = a; return h })
	e.gaon.SetMicroVM(microvm("m1", 1, node, api.RunAlways))
	eventually(t, "m1-e1 runs", e.running("m1-e1"))
	e.mvmd.FailDeletes(true)

	e.gaon.MarkDeleting("m1")

	eventually(t, "the agent tried to delete", func() bool { return slices.Contains(e.mvmd.Calls(), "DeleteMachine m1-e1") })
	before := h.puts.Load()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, []string{microvms.Finalizer}, e.finalizers("m1"), "the finalizer stays while mvmd holds the MicroVM")
	assert.Equal(t, before, h.puts.Load(), "no write of the finalizers is attempted")

	e.mvmd.FailDeletes(false)
	eventually(t, "released, the MicroVM is removed", func() bool { return e.gaon.MicroVM("m1").Metadata.Name == "" })
}

func TestRun_KeepsItsFinalizerOnADeletingMicroVMBoundToAnotherNode(t *testing.T) {
	e := start(t, true, nil)
	elsewhere := microvm("elsewhere", 1, "n2", api.RunAlways)
	elsewhere.Metadata.Finalizers = []string{microvms.Finalizer}
	e.gaon.SetMicroVM(elsewhere)
	e.gaon.SetMicroVM(microvm("mine", 1, node, api.RunAlways))
	eventually(t, "mine runs", e.running("mine-e1"))

	e.gaon.MarkDeleting("elsewhere")
	e.gaon.MarkDeleting("mine")

	eventually(t, "control: mine is released and removed", func() bool { return e.gaon.MicroVM("mine").Metadata.Name == "" })
	assert.Equal(t, []string{microvms.Finalizer}, e.finalizers("elsewhere"), "a MicroVM bound to another Node is not this agent's to release")
}

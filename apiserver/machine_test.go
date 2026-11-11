package apiserver_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestPutMachine_CreateSetsPendingAndGeneration(t *testing.T) {
	e := newEnv(t)
	m := machine("m1")
	m.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 4}
	m.Metadata.Generation = 9
	now := time.Now().UTC()
	m.Metadata.DeletionTimestamp = &now

	created, err := e.api.PutMachine(ctx(t), m)
	require.NoError(t, err)

	assert.Equal(t, api.MachinePending, created.Status.Phase)
	assert.Zero(t, created.Status.BindingEpoch)
	assert.EqualValues(t, 1, created.Metadata.Generation)
	assert.Nil(t, created.Metadata.DeletionTimestamp)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	got, err := e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	assert.Equal(t, created, got)
}

func TestPutMachine_CreateRefusesBindingFields(t *testing.T) {
	e := newEnv(t)
	named := machine("named")
	named.Spec.NodeName = "n1"
	_, err := e.api.PutMachine(ctx(t), named)
	assert.ErrorIs(t, err, api.ErrInvalid)

	epoch := machine("epoch")
	epoch.Spec.BindingEpoch = 1
	_, err = e.api.PutMachine(ctx(t), epoch)
	assert.ErrorIs(t, err, api.ErrInvalid)

	_, err = e.api.GetMachine(ctx(t), "named")
	assert.ErrorIs(t, err, api.ErrNotFound, "a refused create stores nothing")
	_, err = e.api.PutMachine(ctx(t), machine("plain"))
	assert.NoError(t, err)
}

func TestPutMachine_CreateExistingIsAlreadyExists(t *testing.T) {
	e := newEnv(t)
	first := e.createMachine(t, machine("m1"))

	again := machine("m1")
	again.Spec.Image = "other"
	_, err := e.api.PutMachine(ctx(t), again)

	assert.ErrorIs(t, err, api.ErrAlreadyExists)
	got, err := e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	assert.Equal(t, first, got)
}

func TestPutMachine_UpdateIsCompareAndSwapOnVersion(t *testing.T) {
	e := newEnv(t)
	first := e.createMachine(t, machine("m1"))

	update := *first
	update.Spec.RunStrategy = api.RunHalted
	updated, err := e.api.PutMachine(ctx(t), &update)
	require.NoError(t, err)
	assert.NotEqual(t, first.Metadata.ResourceVersion, updated.Metadata.ResourceVersion)
	assert.Equal(t, api.RunHalted, updated.Spec.RunStrategy)

	stale := *first
	stale.Spec.RunStrategy = api.RunOnce
	_, err = e.api.PutMachine(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "the first version is no longer current")
	got, err := e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	assert.Equal(t, api.RunHalted, got.Spec.RunStrategy)

	missing := machine("absent")
	missing.Metadata.ResourceVersion = "7"
	_, err = e.api.PutMachine(ctx(t), missing)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestPutMachine_UpdateWritesSpecOnly(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	m = e.bind(t, m, "n1", 1)
	status := *m
	status.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}
	m, err := e.api.PutMachineStatus(ctx(t), &status)
	require.NoError(t, err)

	update := *m
	update.Spec.RunStrategy = api.RunHalted
	update.Status = api.MachineStatus{Phase: api.MachineFailed, BindingEpoch: 1}
	updated, err := e.api.PutMachine(ctx(t), &update)
	require.NoError(t, err)

	assert.Equal(t, api.MachineRunning, updated.Status.Phase, "a spec write does not change the status")
	assert.Equal(t, "n1", updated.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), updated.Spec.BindingEpoch)
}

func TestPutMachine_UpdateRefusesFrozenSpecFields(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	m = e.bind(t, m, "n1", 1)

	for name, change := range map[string]func(*api.Machine){
		"image":        func(m *api.Machine) { m.Spec.Image = "other" },
		"nodeName":     func(m *api.Machine) { m.Spec.NodeName = "n2" },
		"bindingEpoch": func(m *api.Machine) { m.Spec.BindingEpoch = 5 },
	} {
		t.Run(name, func(t *testing.T) {
			update := *m
			change(&update)
			_, err := e.api.PutMachine(ctx(t), &update)
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
	update := *m
	update.Spec.IdleTimeoutSeconds = 30
	_, err := e.api.PutMachine(ctx(t), &update)
	assert.NoError(t, err, "a mutable field is accepted")
}

func TestPutMachine_GenerationFollowsSpec(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))

	same, err := e.api.PutMachine(ctx(t), m)
	require.NoError(t, err)
	assert.EqualValues(t, 1, same.Metadata.Generation, "no spec change, no new generation")

	changed := *same
	changed.Spec.RunStrategy = api.RunHalted
	changed.Metadata.Generation = 40
	next, err := e.api.PutMachine(ctx(t), &changed)
	require.NoError(t, err)
	assert.EqualValues(t, 2, next.Metadata.Generation)
}

func TestPutMachine_DeletionTimestampIsNotWrittenByPut(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	now := time.Now().UTC()
	m.Metadata.DeletionTimestamp = &now

	updated, err := e.api.PutMachine(ctx(t), m)

	require.NoError(t, err)
	assert.Nil(t, updated.Metadata.DeletionTimestamp)
}

func TestPutMachine_RefusesNamesThatAreNotThePath(t *testing.T) {
	e := newEnv(t)
	status, _ := e.send(t, http.MethodPut, "/machines/other", `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"Machine","metadata":{"name":"m1"},"spec":{"runStrategy":"Always"},"status":{}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, status)

	_, err := e.api.PutMachine(ctx(t), machine("a/b"))
	assert.ErrorIs(t, err, api.ErrInvalid, "a slash would address another key")
	_, err = e.api.PutMachine(ctx(t), machine("ab"))
	assert.NoError(t, err)
}

func TestWrites_RefuseMalformedBodies(t *testing.T) {
	e := newEnv(t)
	const valid = `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"Machine","metadata":{"name":"m1"},"spec":{"runStrategy":"Always"},"status":{}}`

	status, _ := e.send(t, http.MethodPut, "/machines/m1", `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"Machine","metadata":{"name":"m1"},"spec":{"runStrategy":"Always","colour":"red"},"status":{}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, status, "an unknown field")
	status, _ = e.send(t, http.MethodPut, "/machines/m1", valid+valid)
	assert.Equal(t, http.StatusUnprocessableEntity, status, "two values")
	status, _ = e.send(t, http.MethodPut, "/machines/m1", `{`)
	assert.Equal(t, http.StatusUnprocessableEntity, status, "truncated")
	status, _ = e.send(t, http.MethodPut, "/machines/m1", valid)
	assert.Equal(t, http.StatusOK, status, "the same body without the fault")
}

func TestBindMachine_SetsNodeAndEpoch(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))

	first := e.bind(t, m, "n1", 1)
	assert.Equal(t, "n1", first.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), first.Spec.BindingEpoch)
	assert.EqualValues(t, 2, first.Metadata.Generation, "the binding changes the spec")

	second := e.bind(t, first, "n2", 2)
	assert.Equal(t, "n2", second.Spec.NodeName)
	assert.Equal(t, api.Epoch(2), second.Spec.BindingEpoch)
}

func TestBindMachine_RefusesWrongEpochAndNode(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	m = e.bind(t, m, "n1", 1)

	for name, b := range map[string]api.Binding{
		"skipped epoch": {NodeName: "n2", BindingEpoch: 3},
		"same epoch":    {NodeName: "n2", BindingEpoch: 1},
		"no node":       {NodeName: "", BindingEpoch: 2},
	} {
		t.Run(name, func(t *testing.T) {
			b.ResourceVersion = m.Metadata.ResourceVersion
			_, err := e.api.BindMachine(ctx(t), "m1", b)
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
	got, err := e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	assert.Equal(t, m, got, "refused bindings change nothing")
	_ = e.bind(t, m, "n2", 2)
}

func TestBindMachine_IsCompareAndSwapOnVersion(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	stale := m.Metadata.ResourceVersion
	m = e.bind(t, m, "n1", 1)

	_, err := e.api.BindMachine(ctx(t), "m1", api.Binding{ResourceVersion: stale, NodeName: "n2", BindingEpoch: 2})
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = e.api.BindMachine(ctx(t), "m1", api.Binding{NodeName: "n2", BindingEpoch: 2})
	assert.ErrorIs(t, err, api.ErrConflict, "a binding without a version")
	_, err = e.api.BindMachine(ctx(t), "absent", api.Binding{ResourceVersion: "1", NodeName: "n2", BindingEpoch: 1})
	assert.ErrorIs(t, err, api.ErrNotFound)
	_ = e.bind(t, m, "n2", 2)
}

func TestPutMachineStatus_RefusesStaleEpoch(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	m = e.bind(t, m, "n1", 1)

	stale := *m
	stale.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 0}
	_, err := e.api.PutMachineStatus(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrInvalid)

	current := *m
	current.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}
	written, err := e.api.PutMachineStatus(ctx(t), &current)
	require.NoError(t, err)
	assert.Equal(t, api.MachineRunning, written.Status.Phase)
}

func TestPutMachineStatus_WritesStatusOnly(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))
	m = e.bind(t, m, "n1", 1)

	next := *m
	next.Spec.Image = "other"
	next.Spec.NodeName = "n9"
	next.Metadata.Finalizers = []string{"x"}
	next.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}
	written, err := e.api.PutMachineStatus(ctx(t), &next)
	require.NoError(t, err)

	assert.Equal(t, api.MachineRunning, written.Status.Phase)
	assert.Equal(t, "alpine", written.Spec.Image)
	assert.Equal(t, "n1", written.Spec.NodeName)
	assert.Empty(t, written.Metadata.Finalizers)
}

func TestPutMachineStatus_RefusesUnknownPhaseAndStaleVersion(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))

	bogus := *m
	bogus.Status = api.MachineStatus{Phase: "Dancing"}
	_, err := e.api.PutMachineStatus(ctx(t), &bogus)
	assert.ErrorIs(t, err, api.ErrInvalid)

	stale := *m
	stale.Status = api.MachineStatus{Phase: api.MachineRunning}
	later, err := e.api.PutMachineStatus(ctx(t), &stale)
	require.NoError(t, err)
	_, err = e.api.PutMachineStatus(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "the version moved with the first status")

	noVersion := *later
	noVersion.Metadata.ResourceVersion = ""
	_, err = e.api.PutMachineStatus(ctx(t), &noVersion)
	assert.ErrorIs(t, err, api.ErrConflict)

	absent := machine("absent")
	absent.Metadata.ResourceVersion = "3"
	absent.Status = api.MachineStatus{Phase: api.MachinePending}
	_, err = e.api.PutMachineStatus(ctx(t), absent)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestDeleteMachine_WithoutFinalizersRemovesIt(t *testing.T) {
	e := newEnv(t)
	m := e.createMachine(t, machine("m1"))

	_, err := e.api.DeleteMachine(ctx(t), "m1", "1")
	assert.ErrorIs(t, err, api.ErrConflict, "a version that is not current")
	status, _ := e.send(t, http.MethodDelete, "/machines/m1", "")
	assert.Equal(t, http.StatusConflict, status, "no version")
	_, err = e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err, "refused deletes remove nothing")

	deleted, err := e.api.DeleteMachine(ctx(t), "m1", m.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.NotNil(t, deleted.Metadata.DeletionTimestamp)
	_, err = e.api.GetMachine(ctx(t), "m1")
	assert.ErrorIs(t, err, api.ErrNotFound)
	_, err = e.api.DeleteMachine(ctx(t), "m1", m.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestDeleteMachine_FinalizersHoldItUntilTheLastIsCleared(t *testing.T) {
	e := newEnv(t)
	m := machine("m1")
	m.Metadata.Finalizers = []string{"node-agent", "volumes"}
	m = e.createMachine(t, m)

	deleting, err := e.api.DeleteMachine(ctx(t), "m1", m.Metadata.ResourceVersion)
	require.NoError(t, err)
	require.NotNil(t, deleting.Metadata.DeletionTimestamp)
	again, err := e.api.DeleteMachine(ctx(t), "m1", deleting.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Equal(t, deleting.Metadata.DeletionTimestamp, again.Metadata.DeletionTimestamp, "a second delete keeps the first time")

	added := *again
	added.Metadata.Finalizers = []string{"node-agent", "volumes", "late"}
	_, err = e.api.PutMachine(ctx(t), &added)
	assert.ErrorIs(t, err, api.ErrInvalid, "no finalizer is added once deletion began")

	first := *again
	first.Metadata.Finalizers = []string{"volumes"}
	held, err := e.api.PutMachine(ctx(t), &first)
	require.NoError(t, err)
	assert.Equal(t, []string{"volumes"}, held.Metadata.Finalizers)
	still, err := e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err, "one finalizer is left")
	assert.NotNil(t, still.Metadata.DeletionTimestamp)

	last := *held
	last.Metadata.Finalizers = nil
	_, err = e.api.PutMachine(ctx(t), &last)
	require.NoError(t, err)
	_, err = e.api.GetMachine(ctx(t), "m1")
	assert.ErrorIs(t, err, api.ErrNotFound, "the last finalizer is cleared")
}

func TestDeleteMachine_ClearingFinalizersOfALiveMachineKeepsIt(t *testing.T) {
	e := newEnv(t)
	m := machine("m1")
	m.Metadata.Finalizers = []string{"node-agent"}
	m = e.createMachine(t, m)

	m.Metadata.Finalizers = nil
	cleared, err := e.api.PutMachine(ctx(t), m)
	require.NoError(t, err)
	assert.Empty(t, cleared.Metadata.Finalizers)
	_, err = e.api.GetMachine(ctx(t), "m1")
	assert.NoError(t, err, "only a Machine being deleted goes with its last finalizer")
}

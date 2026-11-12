package apiserver_test

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// machineUsing creates a Machine that mounts the named Volumes.
func (e *env) machineUsing(t *testing.T, name string, volumeNames ...string) *api.Machine {
	t.Helper()
	m := machine(name)
	for _, v := range volumeNames {
		m.Spec.Volumes = append(m.Spec.Volumes, api.MachineVolume{Name: v, MountPath: "/" + v})
	}
	return e.createMachine(t, m)
}

func (e *env) getVolume(t *testing.T, name string) *api.Volume {
	t.Helper()
	v, err := e.api.GetVolume(ctx(t), name)
	require.NoError(t, err)
	return v
}

func binding(m *api.Machine, node string, epoch api.Epoch) api.Binding {
	return api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: node, BindingEpoch: epoch}
}

func TestBindMachine_PinsEachLocalVolumeItUsesInTheSameTransaction(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	e.createVolume(t, "v2")
	e.createVolume(t, "unused")
	remote := volume("remote")
	remote.Spec.StorageClass = "ceph"
	remote.Spec.AccessModes = nil
	_, err := e.api.PutVolume(ctx(t), remote)
	require.NoError(t, err)
	unusedBefore := e.getVolume(t, "unused")
	m := e.machineUsing(t, "m1", "v1", "v2", "v1", "remote")

	bound, err := e.api.BindMachine(ctx(t), "m1", binding(m, "n1", 1))
	require.NoError(t, err)

	for _, name := range []string{"v1", "v2"} {
		v := e.getVolume(t, name)
		assert.Equal(t, "n1", v.Spec.NodeName, name)
		assert.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers, name)
		assert.Equal(t, int64(2), v.Metadata.Generation, name+": the pin changed the spec")
		assert.Equal(t, api.VolumePending, v.Status.Phase, name+": the status is the node agent's")
		assert.Equal(t, bound.Metadata.ResourceVersion, v.Metadata.ResourceVersion, name+": written in the Machine's transaction")
	}
	assert.Equal(t, "n1", mustGet(t, e, "m1").Spec.NodeName)
	assert.Empty(t, e.getVolume(t, "remote").Spec.NodeName, "a class the node agent does not serve is not pinned")
	assert.Empty(t, e.getVolume(t, "remote").Metadata.Finalizers)
	assert.Equal(t, unusedBefore, e.getVolume(t, "unused"), "a Volume the Machine does not use is not written")
}

func TestBindMachine_AVolumeAlreadyPinnedToTheNodeIsLeftAsItIs(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	m := e.machineUsing(t, "m1", "v1")
	bound := e.bind(t, m, "n1", 1)
	pinned := e.getVolume(t, "v1")

	again, err := e.api.BindMachine(ctx(t), "m1", binding(bound, "n1", 2))
	require.NoError(t, err, "a rebinding to the same node")
	assert.Equal(t, pinned, e.getVolume(t, "v1"), "the Volume is not written again")
	assert.Equal(t, api.Epoch(2), again.Spec.BindingEpoch)
}

func TestBindMachine_RefusalsWriteNothing(t *testing.T) {
	deleting := volume("deleting")
	deleting.Metadata.Finalizers = []string{holdFinalizer}
	tests := []struct {
		name    string
		prepare func(t *testing.T, e *env) *api.Machine
		want    error
		// control removes the cause and returns the node the Machine is then
		// bound to.
		control func(t *testing.T, e *env, m *api.Machine) string
	}{
		{
			name: "a Volume pinned to another node",
			prepare: func(t *testing.T, e *env) *api.Machine {
				e.createVolume(t, "v1")
				e.createVolume(t, "v2")
				e.bind(t, e.machineUsing(t, "other", "v1"), "n1", 1)
				return e.machineUsing(t, "m1", "v2", "v1")
			},
			want: api.ErrConflict,
			control: func(t *testing.T, e *env, m *api.Machine) string {
				return "n1"
			},
		},
		{
			name: "a Volume being deleted",
			prepare: func(t *testing.T, e *env) *api.Machine {
				e.createVolume(t, "v2")
				created, err := e.api.PutVolume(ctx(t), deleting)
				require.NoError(t, err)
				_, err = e.api.DeleteVolume(ctx(t), "deleting", created.Metadata.ResourceVersion)
				require.NoError(t, err)
				return e.machineUsing(t, "m1", "v2", "deleting")
			},
			want: api.ErrConflict,
			control: func(t *testing.T, e *env, m *api.Machine) string {
				v := e.getVolume(t, "deleting")
				v.Metadata.Finalizers = nil
				_, err := e.api.PutVolume(ctx(t), v)
				require.NoError(t, err)
				e.createVolume(t, "deleting")
				return "n2"
			},
		},
		{
			name: "a Volume that does not exist",
			prepare: func(t *testing.T, e *env) *api.Machine {
				e.createVolume(t, "v2")
				return e.machineUsing(t, "m1", "v2", "missing")
			},
			want: api.ErrInvalid,
			control: func(t *testing.T, e *env, m *api.Machine) string {
				e.createVolume(t, "missing")
				return "n2"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			m := tc.prepare(t, e)
			machineBefore := mustGet(t, e, "m1")
			volumesBefore, err := e.api.ListVolumes(ctx(t))
			require.NoError(t, err)

			_, err = e.api.BindMachine(ctx(t), "m1", binding(m, "n2", 1))
			assert.ErrorIs(t, err, tc.want)

			assert.Equal(t, machineBefore, mustGet(t, e, "m1"), "the Machine is not bound")
			volumesAfter, err := e.api.ListVolumes(ctx(t))
			require.NoError(t, err)
			assert.Equal(t, volumesBefore.Items, volumesAfter.Items, "no Volume is pinned, not even one that would have been")

			node := tc.control(t, e, m)
			bound, err := e.api.BindMachine(ctx(t), "m1", binding(m, node, 1))
			require.NoError(t, err, "control: the same binding once the cause is gone")
			assert.Equal(t, node, bound.Spec.NodeName)
			assert.Equal(t, node, e.getVolume(t, "v2").Spec.NodeName, "control: the Volume that would have been pinned is")
		})
	}
}

func TestBindMachine_AVolumeChangedAfterItWasReadRefusesTheBinding(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	m := e.machineUsing(t, "m1", "v1")

	// The webhook runs after the binding read the Volume and before it writes,
	// and changes the Volume in between.
	w := newWebhookServer(t)
	var changed atomic.Bool
	w.serve = func(rw http.ResponseWriter, r *http.Request, c call) {
		// The change is itself a write the webhook sees, and passes.
		if changed.CompareAndSwap(false, true) {
			v := e.getVolume(t, "v1")
			v.Metadata.Finalizers = []string{holdFinalizer}
			_, err := e.api.PutVolume(ctx(t), v)
			require.NoError(t, err)
		}
		reply(rw, allow(c))
	}
	e.validating(t, "slow", w.hook("slow.hooks.example", "/", "volumes"))

	_, err := e.api.BindMachine(ctx(t), "m1", binding(m, "n1", 1))
	assert.ErrorIs(t, err, api.ErrConflict)
	assert.Empty(t, e.getVolume(t, "v1").Spec.NodeName, "the Volume is not pinned")
	assert.Empty(t, mustGet(t, e, "m1").Spec.NodeName, "the Machine is not bound")

	retry, err := e.api.BindMachine(ctx(t), "m1", binding(mustGet(t, e, "m1"), "n1", 1))
	require.NoError(t, err, "control: the same binding, with the Volume unchanged since it was read")
	assert.Equal(t, "n1", retry.Spec.NodeName)
}

func TestBindMachine_TwoBindingsOfOneVolumeToTwoNodesPinItOnce(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	m1 := e.machineUsing(t, "m1", "v1")
	m2 := e.machineUsing(t, "m2", "v1")

	// Each binding reads the Volume unpinned before either writes: the
	// webhook holds both until the second has reached it.
	var arrived atomic.Int32
	release := make(chan struct{})
	w := newWebhookServer(t)
	w.serve = func(rw http.ResponseWriter, r *http.Request, c call) {
		if arrived.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		reply(rw, allow(c))
	}
	e.validating(t, "barrier", w.hook("barrier.hooks.example", "/", "volumes"))

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, bind := range []struct {
		m    *api.Machine
		node string
	}{{m1, "n1"}, {m2, "n2"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.api.BindMachine(ctx(t), bind.m.Metadata.Name, binding(bind.m, bind.node, 1))
		}()
	}
	wg.Wait()
	require.Equal(t, int32(2), arrived.Load(), "both bindings read the Volume before either wrote")

	winner := 0
	if errs[0] != nil {
		winner = 1
	}
	loser := 1 - winner
	require.NoError(t, errs[winner])
	assert.ErrorIs(t, errs[loser], api.ErrConflict)

	nodes := []string{"n1", "n2"}
	v := e.getVolume(t, "v1")
	assert.Equal(t, nodes[winner], v.Spec.NodeName, "the Volume is pinned to the node of the binding that won")
	assert.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers)
	names := []string{"m1", "m2"}
	assert.Equal(t, nodes[winner], mustGet(t, e, names[winner]).Spec.NodeName)
	assert.Empty(t, mustGet(t, e, names[loser]).Spec.NodeName, "the loser stays unbound")
}

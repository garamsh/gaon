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

// microvmUsing creates a MicroVM that mounts the named Volumes.
func (e *env) microvmUsing(t *testing.T, name string, volumeNames ...string) *api.MicroVM {
	t.Helper()
	m := microvm(name)
	for _, v := range volumeNames {
		m.Spec.Volumes = append(m.Spec.Volumes, api.MicroVMVolume{Name: v, MountPath: "/" + v})
	}
	return e.createMicroVM(t, m)
}

func (e *env) getVolume(t *testing.T, name string) *api.Volume {
	t.Helper()
	v, err := e.api.GetVolume(ctx(t), name)
	require.NoError(t, err)
	return v
}

func binding(m *api.MicroVM, node string, epoch api.Epoch) api.Binding {
	return api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: node, BindingEpoch: epoch}
}

func TestBindMicroVM_PinsEachLocalVolumeItUsesInTheSameTransaction(t *testing.T) {
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
	m := e.microvmUsing(t, "m1", "v1", "v2", "v1", "remote")

	bound, err := e.api.BindMicroVM(ctx(t), "m1", binding(m, "n1", 1))
	require.NoError(t, err)

	for _, name := range []string{"v1", "v2"} {
		v := e.getVolume(t, name)
		assert.Equal(t, "n1", v.Spec.NodeName, name)
		assert.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers, name)
		assert.Equal(t, int64(2), v.Metadata.Generation, name+": the pin changed the spec")
		assert.Equal(t, api.VolumePending, v.Status.Phase, name+": the status is the node agent's")
		assert.Equal(t, bound.Metadata.ResourceVersion, v.Metadata.ResourceVersion, name+": written in the MicroVM's transaction")
	}
	assert.Equal(t, "n1", mustGet(t, e, "m1").Spec.NodeName)
	assert.Empty(t, e.getVolume(t, "remote").Spec.NodeName, "a class the node agent does not serve is not pinned")
	assert.Empty(t, e.getVolume(t, "remote").Metadata.Finalizers)
	assert.Equal(t, unusedBefore, e.getVolume(t, "unused"), "a Volume the MicroVM does not use is not written")
}

func TestBindMicroVM_AVolumeAlreadyPinnedToTheNodeIsLeftAsItIs(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	m := e.microvmUsing(t, "m1", "v1")
	bound := e.bind(t, m, "n1", 1)
	pinned := e.getVolume(t, "v1")

	again, err := e.api.BindMicroVM(ctx(t), "m1", binding(bound, "n1", 2))
	require.NoError(t, err, "a rebinding to the same node")
	assert.Equal(t, pinned, e.getVolume(t, "v1"), "the Volume is not written again")
	assert.Equal(t, api.Epoch(2), again.Spec.BindingEpoch)
}

func TestBindMicroVM_RefusalsWriteNothing(t *testing.T) {
	deleting := volume("deleting")
	deleting.Metadata.Finalizers = []string{holdFinalizer}
	tests := []struct {
		name    string
		prepare func(t *testing.T, e *env) *api.MicroVM
		want    error
		// control removes the cause and returns the node the MicroVM is then
		// bound to.
		control func(t *testing.T, e *env, m *api.MicroVM) string
	}{
		{
			name: "a Volume pinned to another node",
			prepare: func(t *testing.T, e *env) *api.MicroVM {
				e.createVolume(t, "v1")
				e.createVolume(t, "v2")
				e.bind(t, e.microvmUsing(t, "other", "v1"), "n1", 1)
				return e.microvmUsing(t, "m1", "v2", "v1")
			},
			want: api.ErrConflict,
			control: func(t *testing.T, e *env, m *api.MicroVM) string {
				return "n1"
			},
		},
		{
			name: "a Volume being deleted",
			prepare: func(t *testing.T, e *env) *api.MicroVM {
				e.createVolume(t, "v2")
				created, err := e.api.PutVolume(ctx(t), deleting)
				require.NoError(t, err)
				_, err = e.api.DeleteVolume(ctx(t), "deleting", created.Metadata.ResourceVersion)
				require.NoError(t, err)
				return e.microvmUsing(t, "m1", "v2", "deleting")
			},
			want: api.ErrConflict,
			control: func(t *testing.T, e *env, m *api.MicroVM) string {
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
			prepare: func(t *testing.T, e *env) *api.MicroVM {
				e.createVolume(t, "v2")
				return e.microvmUsing(t, "m1", "v2", "missing")
			},
			want: api.ErrInvalid,
			control: func(t *testing.T, e *env, m *api.MicroVM) string {
				e.createVolume(t, "missing")
				return "n2"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			m := tc.prepare(t, e)
			microvmBefore := mustGet(t, e, "m1")
			volumesBefore, err := e.api.ListVolumes(ctx(t))
			require.NoError(t, err)

			_, err = e.api.BindMicroVM(ctx(t), "m1", binding(m, "n2", 1))
			assert.ErrorIs(t, err, tc.want)

			assert.Equal(t, microvmBefore, mustGet(t, e, "m1"), "the MicroVM is not bound")
			volumesAfter, err := e.api.ListVolumes(ctx(t))
			require.NoError(t, err)
			assert.Equal(t, volumesBefore.Items, volumesAfter.Items, "no Volume is pinned, not even one that would have been")

			node := tc.control(t, e, m)
			bound, err := e.api.BindMicroVM(ctx(t), "m1", binding(m, node, 1))
			require.NoError(t, err, "control: the same binding once the cause is gone")
			assert.Equal(t, node, bound.Spec.NodeName)
			assert.Equal(t, node, e.getVolume(t, "v2").Spec.NodeName, "control: the Volume that would have been pinned is")
		})
	}
}

func TestBindMicroVM_AVolumeChangedAfterItWasReadRefusesTheBinding(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	m := e.microvmUsing(t, "m1", "v1")

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

	_, err := e.api.BindMicroVM(ctx(t), "m1", binding(m, "n1", 1))
	assert.ErrorIs(t, err, api.ErrConflict)
	assert.Empty(t, e.getVolume(t, "v1").Spec.NodeName, "the Volume is not pinned")
	assert.Empty(t, mustGet(t, e, "m1").Spec.NodeName, "the MicroVM is not bound")

	retry, err := e.api.BindMicroVM(ctx(t), "m1", binding(mustGet(t, e, "m1"), "n1", 1))
	require.NoError(t, err, "control: the same binding, with the Volume unchanged since it was read")
	assert.Equal(t, "n1", retry.Spec.NodeName)
}

func TestBindMicroVM_TwoBindingsOfOneVolumeToTwoNodesPinItOnce(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")
	m1 := e.microvmUsing(t, "m1", "v1")
	m2 := e.microvmUsing(t, "m2", "v1")

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
		m    *api.MicroVM
		node string
	}{{m1, "n1"}, {m2, "n2"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.api.BindMicroVM(ctx(t), bind.m.Metadata.Name, binding(bind.m, bind.node, 1))
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

func TestBindMicroVM_AMutatingWebhookMayNotChangeWhatThePinSet(t *testing.T) {
	tests := []struct {
		name  string
		ops   string
		want  error
		check func(t *testing.T, v *api.Volume)
	}{
		{name: "a spec field", ops: `[{"op":"replace","path":"/spec/storage","value":"9Gi"}]`, want: api.ErrInvalid},
		{name: "the node the pin set", ops: `[{"op":"remove","path":"/spec/nodeName"}]`, want: api.ErrInvalid},
		{name: "a finalizer", ops: `[{"op":"add","path":"/metadata/finalizers/-","value":"example.com/extra"}]`, want: api.ErrInvalid},
		{
			name: "the status", ops: `[{"op":"replace","path":"/status/phase","value":"Bound"}]`,
			check: func(t *testing.T, v *api.Volume) {
				assert.Equal(t, api.VolumePending, v.Status.Phase, "the stored status is kept")
			},
		},
		{
			name: "the deletion time", ops: `[{"op":"add","path":"/metadata/deletionTimestamp","value":"2030-01-01T00:00:00Z"}]`,
			check: func(t *testing.T, v *api.Volume) {
				assert.Nil(t, v.Metadata.DeletionTimestamp, "the stored deletion time is kept")
			},
		},
		{
			name: "an owner, as the control that a webhook may change something else", ops: `[{"op":"add","path":"/metadata/ownerReferences","value":[{"apiVersion":"v1","kind":"Owner","name":"o"}]}]`,
			check: func(t *testing.T, v *api.Volume) {
				assert.Len(t, v.Metadata.OwnerReferences, 1, "the webhook's change is stored")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.createVolume(t, "v1")
			m := e.microvmUsing(t, "m1", "v1")
			volumeBefore := e.getVolume(t, "v1")
			microvmBefore := mustGet(t, e, "m1")
			w := newWebhookServer(t)
			w.answer = func(c call) *api.AdmissionResponse { return patch(c, tc.ops) }
			e.mutating(t, "pin", w.hook("pin.hooks.example", "/", "volumes"))

			_, err := e.api.BindMicroVM(ctx(t), "m1", binding(m, "n1", 1))
			if tc.want == nil {
				require.NoError(t, err)
				assert.Equal(t, "n1", e.getVolume(t, "v1").Spec.NodeName)
				tc.check(t, e.getVolume(t, "v1"))
				return
			}
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, volumeBefore, e.getVolume(t, "v1"), "the Volume is not written")
			assert.Equal(t, microvmBefore, mustGet(t, e, "m1"), "the MicroVM is not bound")

			w.answer = func(c call) *api.AdmissionResponse { return allow(c) }
			_, err = e.api.BindMicroVM(ctx(t), "m1", binding(m, "n1", 1))
			assert.NoError(t, err, "control: the same binding once the webhook leaves it alone")
		})
	}
}

func TestBindMicroVM_AVolumeThePinLeavesAsItIsHoldsItsPlaceInTheTransaction(t *testing.T) {
	tests := []struct {
		name string
		// interfere runs between the binding's read of the Volume and its
		// commit; nil is the control.
		interfere func(t *testing.T, e *env)
		want      error
	}{
		{name: "control: nothing changes", want: nil},
		{
			name: "the Volume is changed",
			interfere: func(t *testing.T, e *env) {
				v := e.getVolume(t, "v1")
				v.Metadata.Finalizers = append(v.Metadata.Finalizers, holdFinalizer)
				_, err := e.api.PutVolume(ctx(t), v)
				require.NoError(t, err)
			},
			want: api.ErrConflict,
		},
		{
			name: "the Volume starts to be deleted",
			interfere: func(t *testing.T, e *env) {
				_, err := e.api.DeleteVolume(ctx(t), "v1", e.getVolume(t, "v1").Metadata.ResourceVersion)
				require.NoError(t, err)
			},
			want: api.ErrConflict,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.createVolume(t, "v1")
			bound := e.bind(t, e.microvmUsing(t, "m1", "v1"), "n1", 1)
			pinned := e.getVolume(t, "v1")

			// The rebinding to the same node changes nothing of the Volume. The
			// webhook runs after the Volume was read and before the commit.
			var once atomic.Bool
			w := newWebhookServer(t)
			w.serve = func(rw http.ResponseWriter, r *http.Request, c call) {
				if tc.interfere != nil && once.CompareAndSwap(false, true) {
					tc.interfere(t, e)
				}
				reply(rw, allow(c))
			}
			e.validating(t, "between", w.hook("between.hooks.example", "/", "microvms/binding"))

			_, err := e.api.BindMicroVM(ctx(t), "m1", binding(bound, "n1", 2))
			if tc.want == nil {
				assert.NoError(t, err)
				assert.Equal(t, pinned, e.getVolume(t, "v1"), "the Volume is not written")
				return
			}
			require.NotEmpty(t, w.received(), "the webhook ran between the read and the commit")
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, api.Epoch(1), mustGet(t, e, "m1").Spec.BindingEpoch, "the MicroVM is not rebound")
		})
	}
}

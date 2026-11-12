package api_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newVolume(name string) *api.Volume {
	return &api.Volume{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.VolumeSpec{
			StorageClass: api.StorageClassLocal,
			AccessModes:  []string{api.AccessReadWriteOnce},
			Storage:      "10Gi",
		},
	}
}

func TestVolumeAttach(t *testing.T) {
	tests := []struct {
		name     string
		attached *api.Attachment
		epoch    api.Epoch
		valid    bool
	}{
		{"unattached", nil, 3, true},
		{"attached at a lower epoch", &api.Attachment{MachineName: "old", BindingEpoch: 2}, 3, true},
		{"attached at the same epoch", &api.Attachment{MachineName: "old", BindingEpoch: 3}, 3, false},
		{"attached at a higher epoch", &api.Attachment{MachineName: "old", BindingEpoch: 4}, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newVolume("data")
			v.Status.Attachment = tt.attached
			err := v.Attach("m1", tt.epoch)
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, &api.Attachment{MachineName: "m1", BindingEpoch: tt.epoch}, v.Status.Attachment)
				return
			}
			assert.ErrorIs(t, err, api.ErrConflict)
			assert.Equal(t, tt.attached, v.Status.Attachment, "a refused attach leaves the attachment as it was")
		})
	}
}

func TestVolumeAttachRefusesADeletingVolume(t *testing.T) {
	v := newVolume("data")
	assert.NoError(t, v.Attach("m1", 1), "control: the same volume, not deleting")

	deleting := newVolume("data")
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now
	assert.ErrorIs(t, deleting.Attach("m1", 1), api.ErrConflict)
	assert.Nil(t, deleting.Status.Attachment)
}

func TestVolumeValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(v *api.Volume)
		valid  bool
	}{
		{"local volume", func(v *api.Volume) {}, true},
		{"wrong kind", func(v *api.Volume) { v.Kind = api.KindMachine }, false},
		{"no name", func(v *api.Volume) { v.Metadata.Name = "" }, false},
		{"no storage class", func(v *api.Volume) { v.Spec.StorageClass = "" }, false},
		{"local, no access mode", func(v *api.Volume) { v.Spec.AccessModes = nil }, false},
		{"local, ReadWriteMany", func(v *api.Volume) { v.Spec.AccessModes = []string{"ReadWriteMany"} }, false},
		{"local, two access modes", func(v *api.Volume) {
			v.Spec.AccessModes = []string{api.AccessReadWriteOnce, "ReadOnlyMany"}
		}, false},
		{"local, no storage", func(v *api.Volume) { v.Spec.Storage = "" }, false},
		{"local, zero storage", func(v *api.Volume) { v.Spec.Storage = "0" }, false},
		{"local, data source", func(v *api.Volume) { v.Spec.DataSource = "snap-1" }, false},
		{"other class, other access mode and a data source", func(v *api.Volume) {
			v.Spec.StorageClass = "ceph"
			v.Spec.AccessModes = []string{"ReadWriteMany"}
			v.Spec.DataSource = "snap-1"
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newVolume("data")
			tt.mutate(v)
			err := v.Validate()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestVolumeValidateCreate(t *testing.T) {
	assert.NoError(t, newVolume("data").ValidateCreate(), "control: no nodeName")

	pinned := newVolume("data")
	pinned.Spec.NodeName = "node-a"
	assert.ErrorIs(t, pinned.ValidateCreate(), api.ErrInvalid, "only a binding sets nodeName")
	assert.NoError(t, pinned.Validate(), "a pinned volume is valid once it exists")

	broken := newVolume("data")
	broken.Spec.DataSource = "snap-1"
	assert.ErrorIs(t, broken.ValidateCreate(), api.ErrInvalid, "a create applies Validate")
}

func TestVolumeValidateUpdate(t *testing.T) {
	old := newVolume("data")
	old.Spec.NodeName = "node-a"

	tests := []struct {
		name   string
		mutate func(v *api.Volume)
		valid  bool
	}{
		{"nothing changed", func(v *api.Volume) {}, true},
		{"finalizer added", func(v *api.Volume) { v.Metadata.Finalizers = []string{api.NodeAgentFinalizer} }, true},
		{"status changed", func(v *api.Volume) { v.Status.Phase = api.VolumeBound }, true},
		{"storage changed", func(v *api.Volume) { v.Spec.Storage = "20Gi" }, false},
		{"storage class changed", func(v *api.Volume) { v.Spec.StorageClass = "ceph" }, false},
		{"access modes changed", func(v *api.Volume) { v.Spec.AccessModes = []string{"ReadWriteMany"} }, false},
		{"data source changed", func(v *api.Volume) { v.Spec.DataSource = "snap-1" }, false},
		{"nodeName changed", func(v *api.Volume) { v.Spec.NodeName = "node-b" }, false},
		{"nodeName cleared", func(v *api.Volume) { v.Spec.NodeName = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := newVolume("data")
			next.Spec.NodeName = old.Spec.NodeName
			tt.mutate(next)
			err := next.ValidateUpdate(old)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}

	invalid := newVolume("data")
	invalid.Spec.NodeName = "node-a"
	invalid.Spec.AccessModes = nil
	invalidOld := *invalid
	assert.ErrorIs(t, invalid.ValidateUpdate(&invalidOld), api.ErrInvalid, "an unchanged spec must still validate")
}

func TestVolumePin(t *testing.T) {
	deleting := newVolume("data")
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now
	pinnedElsewhere := newVolume("data")
	pinnedElsewhere.Spec.NodeName = "node-b"
	pinnedHere := newVolume("data")
	pinnedHere.Spec.NodeName = "node-a"
	pinnedHere.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	other := newVolume("data")
	other.Spec.StorageClass = "ceph"

	tests := []struct {
		name       string
		volume     *api.Volume
		wantErr    bool
		wantNode   string
		wantFinals []string
	}{
		{"unpinned local", newVolume("data"), false, "node-a", []string{api.NodeAgentFinalizer}},
		{"pinned to the same node", pinnedHere, false, "node-a", []string{api.NodeAgentFinalizer}},
		{"pinned to another node", pinnedElsewhere, true, "node-b", nil},
		{"deleting", deleting, true, "", nil},
		{"other class", other, false, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.volume.Pin("node-a")
			if tt.wantErr {
				assert.ErrorIs(t, err, api.ErrConflict)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantNode, tt.volume.Spec.NodeName)
			assert.Equal(t, tt.wantFinals, tt.volume.Metadata.Finalizers)
		})
	}
}

func TestMachinePinVolumes(t *testing.T) {
	machine := func(names ...string) *api.Machine {
		m := newMachine("m1")
		for _, n := range names {
			m.Spec.Volumes = append(m.Spec.Volumes, api.MachineVolume{Name: n, MountPath: "/" + n})
		}
		return m
	}
	pinnedB := newVolume("pinned-b")
	pinnedB.Spec.NodeName = "node-b"

	t.Run("pins the unpinned local volumes and reports them", func(t *testing.T) {
		a, b := newVolume("a"), newVolume("b")
		changed, err := machine("a", "b", "a").PinVolumes("node-a", map[string]*api.Volume{"a": a, "b": b})
		require.NoError(t, err)
		assert.Equal(t, []*api.Volume{a, b}, changed, "each volume once, in the machine's order")
		assert.Equal(t, "node-a", a.Spec.NodeName)
		assert.Equal(t, []string{api.NodeAgentFinalizer}, a.Metadata.Finalizers)
		assert.Equal(t, "node-a", b.Spec.NodeName)
	})

	t.Run("a volume already pinned to the node is not reported", func(t *testing.T) {
		here := newVolume("here")
		here.Spec.NodeName = "node-a"
		changed, err := machine("here").PinVolumes("node-a", map[string]*api.Volume{"here": here})
		require.NoError(t, err)
		assert.Empty(t, changed)
	})

	t.Run("a machine with no volumes pins nothing", func(t *testing.T) {
		changed, err := machine().PinVolumes("node-a", nil)
		require.NoError(t, err)
		assert.Empty(t, changed)
	})

	t.Run("a volume pinned to another node refuses and nothing is pinned", func(t *testing.T) {
		fresh := newVolume("fresh")
		_, err := machine("fresh", "pinned-b").PinVolumes("node-a", map[string]*api.Volume{"fresh": fresh, "pinned-b": pinnedB})
		assert.ErrorIs(t, err, api.ErrConflict)
		assert.Empty(t, fresh.Spec.NodeName, "the first volume stays unpinned")
		assert.Empty(t, fresh.Metadata.Finalizers)
	})

	t.Run("a missing volume refuses", func(t *testing.T) {
		fresh := newVolume("fresh")
		_, err := machine("fresh", "gone").PinVolumes("node-a", map[string]*api.Volume{"fresh": fresh})
		assert.ErrorIs(t, err, api.ErrInvalid)
		assert.Empty(t, fresh.Spec.NodeName)
	})

	t.Run("a deleting volume refuses", func(t *testing.T) {
		deleting := newVolume("deleting")
		now := time.Now()
		deleting.Metadata.DeletionTimestamp = &now
		_, err := machine("deleting").PinVolumes("node-a", map[string]*api.Volume{"deleting": deleting})
		assert.ErrorIs(t, err, api.ErrConflict)
	})

	t.Run("a machine rebound to the node its volume is pinned to passes", func(t *testing.T) {
		here := newVolume("here")
		here.Spec.NodeName = "node-a"
		_, err := machine("here").PinVolumes("node-a", map[string]*api.Volume{"here": here})
		assert.NoError(t, err)
		_, err = machine("here").PinVolumes("node-b", map[string]*api.Volume{"here": here})
		assert.ErrorIs(t, err, api.ErrConflict, "a volume pinned to an expired node is not followed to another")
	})
}

func TestVolumeValidateStatus(t *testing.T) {
	phases := []api.VolumePhase{api.VolumePending, api.VolumeAvailable, api.VolumeBound, api.VolumeReleased, api.VolumeFailed}
	for _, phase := range phases {
		v := newVolume("data")
		v.Status.Phase = phase
		assert.NoError(t, v.ValidateStatus(), string(phase))
	}
	for _, phase := range []api.VolumePhase{"", "Mounted"} {
		v := newVolume("data")
		v.Status.Phase = phase
		assert.ErrorIs(t, v.ValidateStatus(), api.ErrInvalid, "%q", phase)
	}
}

func TestClientVolumePutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetVolume(ctx, "data")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutVolume(ctx, newVolume("data"))
	require.NoError(t, err)
	_, err = c.PutVolume(ctx, newVolume("data"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	update := *created
	update.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	updated, err := c.PutVolume(ctx, &update)
	require.NoError(t, err)
	_, err = c.PutVolume(ctx, &update)
	assert.ErrorIs(t, err, api.ErrConflict, "the first update made the version stale")

	got, err := c.GetVolume(ctx, "data")
	require.NoError(t, err)
	assert.Equal(t, []string{api.NodeAgentFinalizer}, got.Metadata.Finalizers)

	list, err := c.ListVolumes(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindVolumeList, list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, updated.Metadata.ResourceVersion, list.Metadata.ResourceVersion)

	_, err = c.DeleteVolume(ctx, "data", "")
	var apiErr *api.APIError
	assert.Error(t, err, "a delete names the version it deletes")
	assert.False(t, errors.As(err, &apiErr), "the client refuses before it sends a request")
	_, err = c.DeleteVolume(ctx, "data", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict)
	deleting, err := c.DeleteVolume(ctx, "data", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.NotNil(t, deleting.Metadata.DeletionTimestamp, "a finalizer keeps the volume, marked")
	got, err = c.GetVolume(ctx, "data")
	require.NoError(t, err)
	assert.NotNil(t, got.Metadata.DeletionTimestamp)

	free, err := c.PutVolume(ctx, newVolume("scratch"))
	require.NoError(t, err)
	_, err = c.DeleteVolume(ctx, "scratch", free.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetVolume(ctx, "scratch")
	assert.ErrorIs(t, err, api.ErrNotFound, "a volume with no finalizer is removed at once")
}

func TestClientWatchVolumesDeliversVolumeEventsOnly(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	_, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)
	created, err := c.PutVolume(ctx, newVolume("data"))
	require.NoError(t, err)

	w, err := c.WatchVolumes(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "data", ev.Object.Metadata.Name, "the machine's event is not delivered to a Volume watch")

	_, err = c.DeleteVolume(ctx, "data", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type)

	s.compact()
	stale, err := c.WatchVolumes(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)

	_, err = c.WatchVolumes(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}

func TestNodeAndLeaseValidate(t *testing.T) {
	node := &api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: "node-a"},
		Status:   api.NodeStatus{State: api.NodeStateDiscovered},
	}
	assert.NoError(t, node.Validate())
	node.Kind = api.KindLease
	assert.ErrorIs(t, node.Validate(), api.ErrInvalid)

	lease := &api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: "node-a"},
	}
	assert.NoError(t, lease.Validate())
	lease.Metadata.Name = ""
	assert.ErrorIs(t, lease.Validate(), api.ErrInvalid)
}

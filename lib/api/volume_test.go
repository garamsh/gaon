package api_test

import (
	"encoding/json"
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
		{"attached at a lower epoch", &api.Attachment{MicroVMName: "old", BindingEpoch: 2}, 3, true},
		{"attached at the same epoch", &api.Attachment{MicroVMName: "old", BindingEpoch: 3}, 3, false},
		{"attached at a higher epoch", &api.Attachment{MicroVMName: "old", BindingEpoch: 4}, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newVolume("data")
			v.Status.Attachment = tt.attached
			err := v.Attach("m1", tt.epoch)
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, &api.Attachment{MicroVMName: "m1", BindingEpoch: tt.epoch}, v.Status.Attachment)
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
		{"wrong kind", func(v *api.Volume) { v.Kind = api.KindMicroVM }, false},
		{"no name", func(v *api.Volume) { v.Metadata.Name = "" }, false},
		{"no storage class", func(v *api.Volume) { v.Spec.StorageClass = "" }, false},
		{"local, no access mode", func(v *api.Volume) { v.Spec.AccessModes = nil }, false},
		{"local, ReadWriteMany", func(v *api.Volume) { v.Spec.AccessModes = []string{"ReadWriteMany"} }, false},
		{"local, two access modes", func(v *api.Volume) {
			v.Spec.AccessModes = []string{api.AccessReadWriteOnce, "ReadOnlyMany"}
		}, false},
		{"local, no storage", func(v *api.Volume) { v.Spec.Storage = "" }, false},
		{"local, zero storage", func(v *api.Volume) { v.Spec.Storage = "0" }, false},
		{"local, data source that is a name", func(v *api.Volume) { v.Spec.DataSource = "snap-1" }, true},
		{"local, data source that is not a name", func(v *api.Volume) { v.Spec.DataSource = "Snap_1" }, false},
		{"local, data source with a slash", func(v *api.Volume) { v.Spec.DataSource = "a/b" }, false},
		{"local, data source and no storage", func(v *api.Volume) {
			v.Spec.DataSource = "snap-1"
			v.Spec.Storage = ""
		}, false},
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
	broken.Spec.AccessModes = nil
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
	holding := newVolume("data")
	holding.Metadata.Finalizers = []string{"example.com/other", api.NodeAgentFinalizer}

	tests := []struct {
		name       string
		volume     *api.Volume
		wantErr    bool
		wantNode   string
		wantFinals []string
	}{
		{"unpinned local", newVolume("data"), false, "node-a", []string{api.NodeAgentFinalizer}},
		{"unpinned local already holding the finalizer", holding, false, "node-a", []string{"example.com/other", api.NodeAgentFinalizer}},
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

func TestMicroVMPinVolumes(t *testing.T) {
	microvm := func(names ...string) *api.MicroVM {
		m := newMicroVM("m1")
		for _, n := range names {
			m.Spec.Volumes = append(m.Spec.Volumes, api.MicroVMVolume{Name: n, MountPath: "/" + n})
		}
		return m
	}
	pinnedB := newVolume("pinned-b")
	pinnedB.Spec.NodeName = "node-b"

	t.Run("pins the unpinned local volumes and reports them", func(t *testing.T) {
		a, b := newVolume("a"), newVolume("b")
		changed, err := microvm("a", "b", "a").PinVolumes("node-a", map[string]*api.Volume{"a": a, "b": b})
		require.NoError(t, err)
		assert.Equal(t, []*api.Volume{a, b}, changed, "each volume once, in the microvm's order")
		assert.Equal(t, "node-a", a.Spec.NodeName)
		assert.Equal(t, []string{api.NodeAgentFinalizer}, a.Metadata.Finalizers)
		assert.Equal(t, "node-a", b.Spec.NodeName)
	})

	t.Run("a volume already pinned to the node is not reported", func(t *testing.T) {
		here := newVolume("here")
		here.Spec.NodeName = "node-a"
		changed, err := microvm("here").PinVolumes("node-a", map[string]*api.Volume{"here": here})
		require.NoError(t, err)
		assert.Empty(t, changed)
	})

	t.Run("a microvm with no volumes pins nothing", func(t *testing.T) {
		changed, err := microvm().PinVolumes("node-a", nil)
		require.NoError(t, err)
		assert.Empty(t, changed)
	})

	t.Run("a volume pinned to another node refuses and nothing is pinned", func(t *testing.T) {
		fresh := newVolume("fresh")
		_, err := microvm("fresh", "pinned-b").PinVolumes("node-a", map[string]*api.Volume{"fresh": fresh, "pinned-b": pinnedB})
		assert.ErrorIs(t, err, api.ErrConflict)
		assert.Empty(t, fresh.Spec.NodeName, "the first volume stays unpinned")
		assert.Empty(t, fresh.Metadata.Finalizers)
	})

	t.Run("a missing volume refuses", func(t *testing.T) {
		fresh := newVolume("fresh")
		_, err := microvm("fresh", "gone").PinVolumes("node-a", map[string]*api.Volume{"fresh": fresh})
		assert.ErrorIs(t, err, api.ErrInvalid)
		assert.Empty(t, fresh.Spec.NodeName)
	})

	t.Run("a deleting volume refuses", func(t *testing.T) {
		deleting := newVolume("deleting")
		now := time.Now()
		deleting.Metadata.DeletionTimestamp = &now
		_, err := microvm("deleting").PinVolumes("node-a", map[string]*api.Volume{"deleting": deleting})
		assert.ErrorIs(t, err, api.ErrConflict)
	})

	t.Run("a microvm rebound to the node its volume is pinned to passes", func(t *testing.T) {
		here := newVolume("here")
		here.Spec.NodeName = "node-a"
		_, err := microvm("here").PinVolumes("node-a", map[string]*api.Volume{"here": here})
		assert.NoError(t, err)
		_, err = microvm("here").PinVolumes("node-b", map[string]*api.Volume{"here": here})
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
	_, err := c.PutMicroVM(ctx, newMicroVM("m1"))
	require.NoError(t, err)
	created, err := c.PutVolume(ctx, newVolume("data"))
	require.NoError(t, err)

	w, err := c.WatchVolumes(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "data", ev.Object.Metadata.Name, "the microvm's event is not delivered to a Volume watch")

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

func TestVolume_AttachmentIsWrittenAndReadUnderMicroVMName(t *testing.T) {
	v := newVolume("data")
	v.Status.Attachment = &api.Attachment{MicroVMName: "web", BindingEpoch: 3}

	out, err := json.Marshal(v)
	require.NoError(t, err)
	var fields struct {
		Status struct {
			Attachment map[string]any `json:"attachment"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal(out, &fields))
	assert.Equal(t, map[string]any{"microvmName": "web", "bindingEpoch": float64(3)}, fields.Status.Attachment)

	var back api.Volume
	require.NoError(t, json.Unmarshal(out, &back))
	require.NotNil(t, back.Status.Attachment, "control: the key written is the key read")
	assert.Equal(t, api.Attachment{MicroVMName: "web", BindingEpoch: 3}, *back.Status.Attachment)
}

func newClass(name, provisioner string, expand bool) *api.StorageClass {
	return &api.StorageClass{
		TypeMeta:             api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClass},
		Metadata:             api.ObjectMeta{Name: name},
		Provisioner:          provisioner,
		AllowVolumeExpansion: expand,
	}
}

// resizeOld is a stored local Volume of 10Gi whose disk holds 10Gi.
func resizeOld() *api.Volume {
	old := newVolume("data")
	old.Spec.NodeName = "node-a"
	old.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10Gi"}
	return old
}

func TestVolumeValidateResizeGrowth(t *testing.T) {
	old := resizeOld()
	grown := *old
	grown.Spec.Storage = "20Gi"

	assert.NoError(t, grown.ValidateResize(old, newClass("local", "local", true)), "growth under a class that allows expansion")
	assert.NoError(t, old.ValidateResize(old, newClass("local", "local", false)), "control: no change needs no expansion")

	same := *old
	same.Spec.Storage = "10240Mi"
	assert.NoError(t, same.ValidateResize(old, newClass("local", "local", false)), "the same size in another unit is no change")
}

func TestVolumeValidateResizeRefusesAShrink(t *testing.T) {
	old := resizeOld()
	class := newClass("local", "local", true)

	shrunk := *old
	shrunk.Spec.Storage = "5Gi"
	assert.ErrorIs(t, shrunk.ValidateResize(old, class), api.ErrInvalid, "below the disk")

	atDisk := *old
	atDisk.Spec.Storage = "10Gi"
	assert.NoError(t, atDisk.ValidateResize(old, class), "control: the size of the disk")

	asked := resizeOld()
	asked.Spec.Storage = "40Gi"
	lowered := *asked
	lowered.Spec.Storage = "20Gi"
	assert.NoError(t, lowered.ValidateResize(asked, class), "a request the disk has not reached may be lowered, above the disk")
	cancelled := *asked
	cancelled.Spec.Storage = "10Gi"
	assert.NoError(t, cancelled.ValidateResize(asked, newClass("local", "local", false)), "and down to the disk, under a class that no longer allows expansion")
	below := *asked
	below.Spec.Storage = "9Gi"
	assert.ErrorIs(t, below.ValidateResize(asked, class), api.ErrInvalid, "but not below the disk")

	pending := newVolume("data")
	pendingLowered := *pending
	pendingLowered.Spec.Storage = "5Gi"
	assert.NoError(t, pendingLowered.ValidateResize(pending, class), "a volume with no disk yet has nothing to shrink")
}

func TestVolumeValidateResizeNeedsAClassThatAllowsExpansion(t *testing.T) {
	old := resizeOld()
	grown := *old
	grown.Spec.Storage = "20Gi"

	assert.ErrorIs(t, grown.ValidateResize(old, newClass("local", "local", false)), api.ErrInvalid, "a class that forbids expansion")
	assert.ErrorIs(t, grown.ValidateResize(old, nil), api.ErrInvalid, "a class that does not exist")
	assert.NoError(t, grown.ValidateResize(old, newClass("local", "local", true)), "control: the same growth under a class that allows it")
}

func TestVolumeValidateResizeChangesStorageOnly(t *testing.T) {
	old := resizeOld()
	class := newClass("local", "local", true)

	tests := []struct {
		name   string
		mutate func(v *api.Volume)
		valid  bool
	}{
		{"storage grown", func(v *api.Volume) { v.Spec.Storage = "20Gi" }, true},
		{"storage grown and access modes changed", func(v *api.Volume) {
			v.Spec.Storage = "20Gi"
			v.Spec.AccessModes = []string{"ReadWriteMany"}
		}, false},
		{"storage grown and nodeName cleared", func(v *api.Volume) {
			v.Spec.Storage = "20Gi"
			v.Spec.NodeName = ""
		}, false},
		{"storage grown and data source set", func(v *api.Volume) {
			v.Spec.Storage = "20Gi"
			v.Spec.DataSource = "snap-1"
		}, false},
		{"storage class changed", func(v *api.Volume) { v.Spec.StorageClass = "ceph" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := *old
			tt.mutate(&next)
			err := next.ValidateResize(old, class)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}

	broken := *old
	broken.Spec.AccessModes = nil
	brokenOld := broken
	assert.ErrorIs(t, broken.ValidateResize(&brokenOld, class), api.ErrInvalid, "an unchanged spec must still validate")
}

func TestVolumeValidateResizeRefusesAChangeOnADeletingVolume(t *testing.T) {
	class := newClass("local", "local", true)
	grown := resizeOld()
	grown.Spec.Storage = "20Gi"

	assert.NoError(t, grown.ValidateResize(resizeOld(), class), "control: the same growth of a volume that is not deleting")

	deleting := resizeOld()
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now
	assert.ErrorIs(t, grown.ValidateResize(deleting, class), api.ErrConflict)
}

func TestVolumeValidateResizeAcceptsUnchangedStorageOnADeletingVolume(t *testing.T) {
	deleting := resizeOld()
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now

	same := resizeOld()
	assert.NoError(t, same.ValidateResize(deleting, nil), "an unchanged storage passes on a deleting volume, under any class")

	grown := resizeOld()
	grown.Spec.Storage = "20Gi"
	assert.ErrorIs(t, grown.ValidateResize(deleting, newClass("local", "local", true)), api.ErrConflict, "control: a change of storage on the same volume is refused")
}

func TestVolumeValidateStatusChecksConditions(t *testing.T) {
	v := newVolume("data")
	v.Status.Phase = api.VolumeAvailable
	v.Status.Conditions = []api.Condition{{Type: api.VolumeConditionResizing, Status: api.ConditionTrue, Reason: "InProgress"}}
	assert.NoError(t, v.ValidateStatus(), "control: a known condition status")

	v.Status.Conditions[0].Status = "Maybe"
	assert.ErrorIs(t, v.ValidateStatus(), api.ErrInvalid)
}

func TestVolumeValidateResizeRefusesAShrinkOfAVolumeFromASnapshotBeforeItHasADisk(t *testing.T) {
	class := newClass("local", "local", true)
	pending := func(dataSource string) *api.Volume {
		v := newVolume("data")
		v.Spec.Storage = "1Gi"
		v.Spec.DataSource = dataSource
		return v
	}

	from := pending("snap-1")
	shrunk := *from
	shrunk.Spec.Storage = "1Mi"
	assert.ErrorIs(t, shrunk.ValidateResize(from, class), api.ErrInvalid, "below the size the snapshot was checked against, with no capacity yet")

	grown := *from
	grown.Spec.Storage = "2Gi"
	assert.NoError(t, grown.ValidateResize(from, class), "control: a growth of the same Volume")

	plain := pending("")
	lowered := *plain
	lowered.Spec.Storage = "1Mi"
	assert.NoError(t, lowered.ValidateResize(plain, class), "control: the same shrink of a Volume with no data source, which has no disk yet")
}

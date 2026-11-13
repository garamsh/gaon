package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newSnapshot(name, volume string) *api.VolumeSnapshot {
	return &api.VolumeSnapshot{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolumeSnapshot},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.VolumeSnapshotSpec{VolumeName: volume},
	}
}

func readySnapshot(name, volume string) *api.VolumeSnapshot {
	s := newSnapshot(name, volume)
	s.Status = api.VolumeSnapshotStatus{
		ReadyToUse:   true,
		RestoreSize:  "10Gi",
		StorageClass: api.StorageClassLocal,
		NodeName:     "node-a",
	}
	return s
}

func TestVolumeSnapshotValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(s *api.VolumeSnapshot)
		valid  bool
	}{
		{"names a volume", func(s *api.VolumeSnapshot) {}, true},
		{"wrong kind", func(s *api.VolumeSnapshot) { s.Kind = api.KindVolume }, false},
		{"no name", func(s *api.VolumeSnapshot) { s.Metadata.Name = "" }, false},
		{"no volume name", func(s *api.VolumeSnapshot) { s.Spec.VolumeName = "" }, false},
		{"volume name in capitals", func(s *api.VolumeSnapshot) { s.Spec.VolumeName = "Data" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSnapshot("snap", "data")
			tt.mutate(s)
			err := s.Validate()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestVolumeSnapshotValidateUpdate(t *testing.T) {
	old := newSnapshot("snap", "data")

	relabelled := newSnapshot("snap", "data")
	relabelled.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	assert.NoError(t, relabelled.ValidateUpdate(old), "control: the same spec with a finalizer added")

	moved := newSnapshot("snap", "other")
	assert.ErrorIs(t, moved.ValidateUpdate(old), api.ErrInvalid, "the source may not change")

	broken := newSnapshot("snap", "")
	brokenOld := *broken
	assert.ErrorIs(t, broken.ValidateUpdate(&brokenOld), api.ErrInvalid, "an unchanged snapshot must still validate")
}

func TestVolumeSnapshotValidateStatus(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(s *api.VolumeSnapshot)
		valid  bool
	}{
		{"ready, local, on a Node", func(s *api.VolumeSnapshot) {}, true},
		{"not ready, nothing else", func(s *api.VolumeSnapshot) { s.Status = api.VolumeSnapshotStatus{} }, true},
		{"not ready, snapshotting", func(s *api.VolumeSnapshot) {
			s.Status = api.VolumeSnapshotStatus{Conditions: []api.Condition{
				{Type: api.VolumeSnapshotConditionSnapshotting, Status: api.ConditionTrue, Reason: "InProgress"},
			}}
		}, true},
		{"condition with an unknown status", func(s *api.VolumeSnapshot) {
			s.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: "Maybe"}}
		}, false},
		{"ready, no restore size", func(s *api.VolumeSnapshot) { s.Status.RestoreSize = "" }, false},
		{"ready, zero restore size", func(s *api.VolumeSnapshot) { s.Status.RestoreSize = "0" }, false},
		{"ready, no storage class", func(s *api.VolumeSnapshot) { s.Status.StorageClass = "" }, false},
		{"ready, local, no Node", func(s *api.VolumeSnapshot) { s.Status.NodeName = "" }, false},
		{"ready, other class, no Node", func(s *api.VolumeSnapshot) {
			s.Status.StorageClass = "ceph"
			s.Status.NodeName = ""
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := readySnapshot("snap", "data")
			tt.mutate(s)
			err := s.ValidateStatus()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestVolumeSnapshotValidateStatusUpdate(t *testing.T) {
	ready := readySnapshot("snap", "data")

	tests := []struct {
		name   string
		old    *api.VolumeSnapshot
		mutate func(s *api.VolumeSnapshot)
		valid  bool
	}{
		{"becoming ready", newSnapshot("snap", "data"), func(s *api.VolumeSnapshot) {}, true},
		{"conditions change while ready", ready, func(s *api.VolumeSnapshot) {
			s.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: api.ConditionFalse}}
		}, true},
		{"restore size in another unit", ready, func(s *api.VolumeSnapshot) { s.Status.RestoreSize = "10240Mi" }, true},
		{"no longer ready, status cleared", ready, func(s *api.VolumeSnapshot) { s.Status = api.VolumeSnapshotStatus{} }, false},
		{"no longer ready, nothing else changed", ready, func(s *api.VolumeSnapshot) { s.Status.ReadyToUse = false }, false},
		{"restore size changed", ready, func(s *api.VolumeSnapshot) { s.Status.RestoreSize = "20Gi" }, false},
		{"storage class changed", ready, func(s *api.VolumeSnapshot) { s.Status.StorageClass = "ceph" }, false},
		{"Node changed", ready, func(s *api.VolumeSnapshot) { s.Status.NodeName = "node-b" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := readySnapshot("snap", "data")
			tt.mutate(s)
			err := s.ValidateStatusUpdate(tt.old)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestVolumeSnapshotJSON(t *testing.T) {
	b, err := json.Marshal(readySnapshot("snap", "data"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"VolumeSnapshot","metadata":{"name":"snap"},"spec":{"volumeName":"data"},"status":{"readyToUse":true,"restoreSize":"10Gi","storageClass":"local","nodeName":"node-a"}}`, string(b))

	b, err = json.Marshal(newSnapshot("snap", "data"))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"readyToUse":false`, "a snapshot not ready says so")
}

func volumeFromSnapshot(storage api.Quantity) *api.Volume {
	v := newVolume("restored")
	v.Spec.DataSource = "snap"
	v.Spec.Storage = storage
	return v
}

func TestVolumeCreateFromSnapshotSize(t *testing.T) {
	tests := []struct {
		name    string
		storage api.Quantity
		valid   bool
	}{
		{"as large as the snapshot", "10Gi", true},
		{"equal in another unit", "10240Mi", true},
		{"larger than the snapshot", "20Gi", true},
		{"smaller than the snapshot", "5Gi", false},
		{"one byte below the snapshot", "10737418239", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := volumeFromSnapshot(tt.storage)
			err := v.CreateFromSnapshot(readySnapshot("snap", "data"))
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
				assert.Empty(t, v.Spec.NodeName, "a refused create pins nothing")
				assert.Empty(t, v.Metadata.Finalizers)
			}
		})
	}
}

func TestVolumeCreateFromSnapshotPinsALocalVolumeToTheSnapshotsNode(t *testing.T) {
	v := volumeFromSnapshot("10Gi")
	require.NoError(t, v.CreateFromSnapshot(readySnapshot("snap", "data")))
	assert.Equal(t, "node-a", v.Spec.NodeName)
	assert.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers)

	other := volumeFromSnapshot("10Gi")
	other.Spec.StorageClass = "ceph"
	s := readySnapshot("snap", "data")
	s.Status.StorageClass = "ceph"
	s.Status.NodeName = ""
	require.NoError(t, other.CreateFromSnapshot(s), "control: a class that is not local")
	assert.Empty(t, other.Spec.NodeName, "a class that is not local is not pinned by its snapshot")
	assert.Empty(t, other.Metadata.Finalizers)
}

func TestVolumeCreateFromSnapshotRefusals(t *testing.T) {
	deleting := readySnapshot("snap", "data")
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now

	tests := []struct {
		name     string
		snapshot *api.VolumeSnapshot
		mutate   func(v *api.Volume)
		want     error
	}{
		{"ready snapshot of the same class", readySnapshot("snap", "data"), func(v *api.Volume) {}, nil},
		{"no snapshot", nil, func(v *api.Volume) {}, api.ErrInvalid},
		{"snapshot not ready", newSnapshot("snap", "data"), func(v *api.Volume) {}, api.ErrConflict},
		{"snapshot being deleted", deleting, func(v *api.Volume) {}, api.ErrConflict},
		{"another class than the snapshot's", readySnapshot("snap", "data"), func(v *api.Volume) {
			v.Spec.StorageClass = "ceph"
		}, api.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := volumeFromSnapshot("10Gi")
			tt.mutate(v)
			err := v.CreateFromSnapshot(tt.snapshot)
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
			assert.Empty(t, v.Spec.NodeName)
		})
	}
}

func TestVolumeCreateFromSnapshotLeavesAVolumeWithoutADataSourceAlone(t *testing.T) {
	v := newVolume("plain")
	assert.NoError(t, v.CreateFromSnapshot(nil))
	assert.Empty(t, v.Spec.NodeName)
}

// otherClassFromSnapshot is a Volume of a class that is not local, since
// Validate still refuses a data source for local.
func otherClassFromSnapshot() *api.Volume {
	v := volumeFromSnapshot("10Gi")
	v.Spec.StorageClass = "ceph"
	return v
}

func TestVolumeDataSourceIsImmutable(t *testing.T) {
	old := otherClassFromSnapshot()
	same := otherClassFromSnapshot()
	assert.NoError(t, same.ValidateUpdate(old), "control: an unchanged data source")
	assert.NoError(t, same.ValidateResize(old, newClass("ceph", "ceph", true)), "control: an unchanged data source")

	moved := otherClassFromSnapshot()
	moved.Spec.DataSource = "other"
	assert.ErrorIs(t, moved.ValidateUpdate(old), api.ErrInvalid)
	assert.ErrorIs(t, moved.ValidateResize(old, newClass("ceph", "ceph", true)), api.ErrInvalid)

	cleared := otherClassFromSnapshot()
	cleared.Spec.DataSource = ""
	assert.ErrorIs(t, cleared.ValidateUpdate(old), api.ErrInvalid)
	assert.ErrorIs(t, cleared.ValidateResize(old, newClass("ceph", "ceph", true)), api.ErrInvalid)
}

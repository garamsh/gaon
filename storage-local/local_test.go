package storagelocal_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	storagelocal "github.com/garamsh/gaon/storage-local"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const node = "n1"

var image = "registry.example/app@sha256:" + strings.Repeat("a", 64)

type rig struct {
	gaon  *fakeVolumes
	mvmd  *fakeDisks
	local *storagelocal.Local
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{gaon: newFakeVolumes(), mvmd: newFakeDisks()}
	r.local = storagelocal.NewLocal(node, 10*time.Millisecond, r.gaon, r.gaon, r.mvmd, storagelocal.SystemSleeper{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return r
}

// run runs the Local's loop until the test ends.
func (r *rig) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.local.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func volume(name, nodeName string) api.Volume {
	v := api.Volume{
		Metadata: api.ObjectMeta{Name: name, Generation: 1},
		Spec: api.VolumeSpec{
			StorageClass: api.StorageClassLocal, AccessModes: []string{api.AccessReadWriteOnce},
			Storage: "10Mi", NodeName: nodeName,
		},
		Status: api.VolumeStatus{Phase: api.VolumePending},
	}
	if nodeName != "" {
		v.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	}
	return v
}

func microvmUsing(name string, epoch api.Epoch, volumes ...string) api.MicroVM {
	m := api.MicroVM{
		Metadata: api.ObjectMeta{Name: name, Generation: 1},
		Spec: api.MicroVMSpec{
			Image:        image,
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "2", Memory: "1Gi"}},
			RunStrategy:  api.RunAlways,
			NodeName:     node,
			BindingEpoch: epoch,
		},
	}
	for _, v := range volumes {
		m.Spec.Volumes = append(m.Spec.Volumes, api.MicroVMVolume{Name: v, MountPath: "/mnt/" + v})
	}
	return m
}

func (r *rig) volume(t *testing.T, name string) api.Volume {
	t.Helper()
	v, ok := r.gaon.Volume(name)
	require.True(t, ok, "volume %s exists", name)
	return v
}

func (r *rig) phase(name string, phase api.VolumePhase) func() bool {
	return func() bool {
		v, ok := r.gaon.Volume(name)
		return ok && v.Status.Phase == phase
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, what)
}

func refusal(t *testing.T, err error) *plugin.Refusal {
	t.Helper()
	var ref *plugin.Refusal
	require.ErrorAs(t, err, &ref)
	return ref
}

func TestLocal_AttachMakesTheDiskAndBindsTheVolumeToTheMicroVM(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	r.gaon.SetVolume(volume("other", node))
	m := microvmUsing("db", 1, "data")
	r.gaon.SetMicroVM(m)

	disks, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	require.Len(t, disks, 1)
	assert.Equal(t, "volume-data", disks[0].DiskID)
	disk, ok := r.mvmd.Disk("volume-data")
	require.True(t, ok)
	assert.EqualValues(t, 10<<20, disk.GetSizeBytes())
	v := r.volume(t, "data")
	assert.Equal(t, api.VolumeBound, v.Status.Phase)
	assert.Equal(t, &api.Attachment{MicroVMName: "db", BindingEpoch: 1}, v.Status.Attachment)
	assert.Equal(t, api.Quantity("10485760"), v.Status.Capacity)
	_, ok = r.mvmd.Disk("volume-other")
	assert.False(t, ok, "control: a Volume the MicroVM does not name gets no disk")
	assert.Equal(t, api.VolumePending, r.volume(t, "other").Status.Phase)
}

func TestLocal_AttachIsRepeatableForTheSameMicroVMAndEpoch(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	m := microvmUsing("db", 1, "data", "data")
	r.gaon.SetMicroVM(m)

	first, err := r.local.Attach(context.Background(), &m)
	require.NoError(t, err)
	// A Run round between Attach and the create reads the disk as unheld.
	v := r.volume(t, "data")
	v.Status.Phase = api.VolumeReleased
	r.gaon.SetVolume(v)
	second, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	assert.Len(t, first, 1, "a Volume named twice is one disk")
	assert.Equal(t, first[0].DiskID, second[0].DiskID)
	assert.Equal(t, []string{"CreateDisk volume-data"}, r.mvmd.Calls(), "the disk is made once")
	assert.Equal(t, api.VolumeBound, r.volume(t, "data").Status.Phase, "the second call writes Bound again")
	assert.Equal(t, &api.Attachment{MicroVMName: "db", BindingEpoch: 1}, r.volume(t, "data").Status.Attachment)
}

func TestLocal_AttachRefusesAVolumeItCannotServeAndServesTheOnesItCan(t *testing.T) {
	deleting := volume("deleting", node)
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now
	other := volume("other-class", node)
	other.Spec.StorageClass = "ceph"
	inUse := volume("in-use", node)
	inUse.Status = api.VolumeStatus{Phase: api.VolumeBound, Attachment: &api.Attachment{MicroVMName: "web", BindingEpoch: 1}}
	tests := []struct {
		name   string
		volume string
		seed   []api.Volume
		reason string
	}{
		{"a missing Volume", "missing", nil, storagelocal.ReasonVolumeNotFound},
		{"a Volume of another class", "other-class", []api.Volume{other}, storagelocal.ReasonUnsupportedStorageClass},
		{"a Volume being deleted", "deleting", []api.Volume{deleting}, storagelocal.ReasonVolumeNotUsable},
		{"a Volume pinned to another Node", "elsewhere", []api.Volume{volume("elsewhere", "n2")}, storagelocal.ReasonVolumeNotUsable},
		{"a Volume attached to another MicroVM at the same epoch", "in-use", []api.Volume{inUse}, storagelocal.ReasonVolumeInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.gaon.SetVolume(volume("good", node))
			for _, v := range tt.seed {
				r.gaon.SetVolume(v)
			}
			m := microvmUsing("db", 1, "good", tt.volume)
			r.gaon.SetMicroVM(m)

			_, err := r.local.Attach(context.Background(), &m)

			assert.Equal(t, tt.reason, refusal(t, err).Reason)
			_, made := r.mvmd.Disk("volume-" + tt.volume)
			assert.False(t, made, "no disk is made for the refused Volume")

			control := microvmUsing("web", 1, "good")
			r.gaon.SetMicroVM(control)
			disks, err := r.local.Attach(context.Background(), &control)
			require.NoError(t, err, "control: a MicroVM naming only the good Volume is served")
			assert.Equal(t, "volume-good", disks[0].DiskID)
		})
	}
}

func TestLocal_AttachTakesAVolumeAttachedAtALowerEpoch(t *testing.T) {
	r := newRig(t)
	v := volume("data", node)
	v.Status = api.VolumeStatus{Phase: api.VolumeReleased, Attachment: &api.Attachment{MicroVMName: "db", BindingEpoch: 1}}
	r.gaon.SetVolume(v)
	r.mvmd.AddDisk("volume-data", 10<<20)
	m := microvmUsing("db", 2, "data")
	r.gaon.SetMicroVM(m)

	_, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	assert.Equal(t, &api.Attachment{MicroVMName: "db", BindingEpoch: 2}, r.volume(t, "data").Status.Attachment)
	assert.NotContains(t, r.mvmd.Calls(), "CreateDisk volume-data", "control: the disk that exists is not made again")
}

func TestLocal_AttachWritesFailedAndRefusesWhenTheDiskCannotBeMade(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	m := microvmUsing("db", 1, "data")
	r.gaon.SetMicroVM(m)
	r.mvmd.FailDiskCreates(true)

	_, err := r.local.Attach(context.Background(), &m)

	assert.Equal(t, storagelocal.ReasonVolumeFailed, refusal(t, err).Reason)
	assert.Equal(t, api.VolumeFailed, r.volume(t, "data").Status.Phase)
	assert.Nil(t, r.volume(t, "data").Status.Attachment)

	r.mvmd.FailDiskCreates(false)
	_, err = r.local.Attach(context.Background(), &m)
	require.NoError(t, err, "control: the same MicroVM is served once the disk can be made")
	assert.Equal(t, api.VolumeBound, r.volume(t, "data").Status.Phase)
}

func TestLocal_AttachWritesFailedOnlyForTheOutcomeKindsThatRefuseTheDisk(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	m := microvmUsing("db", 1, "data")
	r.gaon.SetMicroVM(m)

	r.mvmd.FailDiskCreatesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED)
	_, err := r.local.Attach(context.Background(), &m)
	assert.Equal(t, storagelocal.ReasonVolumeFailed, refusal(t, err).Reason)
	assert.Equal(t, api.VolumeFailed, r.volume(t, "data").Status.Phase)

	// Control: a kind outside REJECTED, PRECONDITION_FAILED and FAILED is
	// retried, not written as Failed.
	r.gaon.SetVolume(volume("data", node))
	r.mvmd.FailDiskCreatesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSUPPORTED)
	_, err = r.local.Attach(context.Background(), &m)
	require.Error(t, err)
	var ref *plugin.Refusal
	assert.False(t, errors.As(err, &ref), "the error is not a refusal")
	assert.Equal(t, api.VolumePending, r.volume(t, "data").Status.Phase, "no Failed is written")

	r.mvmd.FailDiskCreatesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED)
	_, err = r.local.Attach(context.Background(), &m)
	require.NoError(t, err, "served once creates succeed")
	assert.Equal(t, api.VolumeBound, r.volume(t, "data").Status.Phase)
}

func TestLocal_AttachRoundsAFractionalSizeUpToWholeBytes(t *testing.T) {
	r := newRig(t)
	v := volume("data", node)
	v.Spec.Storage = "1001m"
	r.gaon.SetVolume(v)
	whole := volume("whole", node)
	whole.Spec.Storage = "3"
	r.gaon.SetVolume(whole)
	m := microvmUsing("db", 1, "data", "whole")
	r.gaon.SetMicroVM(m)

	_, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	disk, ok := r.mvmd.Disk("volume-data")
	require.True(t, ok)
	assert.EqualValues(t, 2, disk.GetSizeBytes(), "1.001 bytes is 2")
	disk, _ = r.mvmd.Disk("volume-whole")
	assert.EqualValues(t, 3, disk.GetSizeBytes(), "control: a whole number is not rounded")
}

func TestLocal_RunWritesAvailableWithCapacityOnceTheDiskExists(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	r.gaon.SetVolume(volume("elsewhere", "n2"))
	r.gaon.SetVolume(volume("unpinned", ""))
	r.run(t)

	eventually(t, "data is Available", r.phase("data", api.VolumeAvailable))

	assert.Equal(t, api.Quantity("10485760"), r.volume(t, "data").Status.Capacity)
	disk, ok := r.mvmd.Disk("volume-data")
	require.True(t, ok)
	assert.EqualValues(t, 10<<20, disk.GetSizeBytes())
	for _, name := range []string{"elsewhere", "unpinned"} {
		_, made := r.mvmd.Disk("volume-" + name)
		assert.False(t, made, "control: %s is not pinned to this Node", name)
		assert.Equal(t, api.VolumePending, r.volume(t, name).Status.Phase)
	}
}

func TestLocal_RunWritesFailedWhenTheDiskCannotBeMadeAndAvailableWhenItCan(t *testing.T) {
	r := newRig(t)
	r.mvmd.FailDiskCreates(true)
	r.gaon.SetVolume(volume("data", node))
	r.run(t)

	eventually(t, "data is Failed", r.phase("data", api.VolumeFailed))

	r.mvmd.FailDiskCreates(false)
	eventually(t, "control: data is Available once the disk can be made", r.phase("data", api.VolumeAvailable))
}

func TestLocal_RunWritesBoundWhileAMicroVMHoldsTheDiskAndReleasedKeepingTheAttachmentAfter(t *testing.T) {
	r := newRig(t)
	v := volume("data", node)
	attachment := &api.Attachment{MicroVMName: "db", BindingEpoch: 1}
	v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Attachment: attachment, Capacity: "10485760"}
	r.gaon.SetVolume(v)
	r.mvmd.AddDisk("volume-data", 10<<20)
	r.mvmd.Hold("db-e1", "volume-data")
	r.run(t)

	eventually(t, "data is Bound", r.phase("data", api.VolumeBound))
	assert.Equal(t, attachment, r.volume(t, "data").Status.Attachment)

	r.mvmd.Drop("db-e1")
	eventually(t, "data is Released", r.phase("data", api.VolumeReleased))
	assert.Equal(t, attachment, r.volume(t, "data").Status.Attachment, "Released keeps the attachment")
}

func TestLocal_RunDeletesTheDiskAndRemovesOnlyItsFinalizerOfAVolumeNoMicroVMHolds(t *testing.T) {
	r := newRig(t)
	// The Volumes are listed by name, so "a-held" and "a-unowned" are read
	// before "b-free" in each round.
	for _, name := range []string{"a-held", "a-unowned", "b-free", "c-elsewhere"} {
		nodeName := node
		if name == "c-elsewhere" {
			nodeName = "n2"
		}
		v := volume(name, nodeName)
		v.Metadata.Finalizers = []string{"example.com/keep", api.NodeAgentFinalizer}
		if name == "a-unowned" {
			v.Metadata.Finalizers = []string{"example.com/keep"}
		}
		r.gaon.SetVolume(v)
		r.gaon.MarkVolumeDeleting(name)
	}
	r.mvmd.AddDisk("volume-a-held", 10<<20)
	r.mvmd.AddDisk("volume-a-unowned", 10<<20)
	r.mvmd.AddDisk("volume-b-free", 10<<20)
	r.mvmd.AddDisk("volume-c-elsewhere", 10<<20)
	r.mvmd.Hold("db-e1", "volume-a-held")
	before := r.volume(t, "b-free")
	r.run(t)

	eventually(t, "b-free loses the agent's finalizer", func() bool {
		return len(r.volume(t, "b-free").Metadata.Finalizers) == 1
	})

	free := r.volume(t, "b-free")
	assert.Equal(t, []string{"example.com/keep"}, free.Metadata.Finalizers)
	assert.Equal(t, before.Spec, free.Spec)
	assert.Equal(t, before.Status, free.Status)
	_, ok := r.mvmd.Disk("volume-b-free")
	assert.False(t, ok, "the disk is deleted")
	assert.Equal(t, []string{"example.com/keep", api.NodeAgentFinalizer}, r.volume(t, "a-held").Metadata.Finalizers, "control: a held Volume keeps its finalizers")
	_, ok = r.mvmd.Disk("volume-a-held")
	assert.True(t, ok, "control: a held disk is kept")
	assert.NotContains(t, r.mvmd.Calls(), "DeleteDisk volume-a-held")
	_, ok = r.mvmd.Disk("volume-a-unowned")
	assert.True(t, ok, "control: a Volume without the agent's finalizer is not the agent's to delete")
	assert.Equal(t, []string{"example.com/keep", api.NodeAgentFinalizer}, r.volume(t, "c-elsewhere").Metadata.Finalizers, "control: another Node's Volume is left to its agent")
	_, ok = r.mvmd.Disk("volume-c-elsewhere")
	assert.True(t, ok)
}

func TestLocal_RunLetsAVolumeGoWhenItsLastFinalizerGoesAndKeepsAHeldOneUntilThen(t *testing.T) {
	r := newRig(t)
	// "a-held" is read before "b-free" in each round.
	for _, name := range []string{"a-held", "b-free"} {
		r.gaon.SetVolume(volume(name, node))
		r.mvmd.AddDisk("volume-"+name, 10<<20)
		r.gaon.MarkVolumeDeleting(name)
	}
	r.mvmd.Hold("db-e1", "volume-a-held")
	r.run(t)

	eventually(t, "b-free is gone", func() bool { _, ok := r.gaon.Volume("b-free"); return !ok })
	_, ok := r.mvmd.Disk("volume-a-held")
	assert.True(t, ok, "control: the disk of a held Volume is kept")
	_, exists := r.gaon.Volume("a-held")
	assert.True(t, exists, "control: the held Volume stays")

	r.mvmd.Drop("db-e1")
	eventually(t, "a-held is gone", func() bool { _, ok := r.gaon.Volume("a-held"); return !ok })
	_, ok = r.mvmd.Disk("volume-a-held")
	assert.False(t, ok)
}

func restoredVolume(name, snapshot string) api.Volume {
	v := volume(name, node)
	v.Spec.DataSource = snapshot
	return v
}

func TestLocal_RunMakesTheDiskOfAVolumeFromASnapshotFromThatSnapshotsIdentifierWithNoMicroVM(t *testing.T) {
	r := newRig(t)
	const otherUID = "fedcba9876543210fedcba9876543210"
	for name, id := range map[string]api.UID{"backup": uid, "other": otherUID} {
		s := snapshotOf(name, "source", id)
		s.Status = api.VolumeSnapshotStatus{ReadyToUse: true, RestoreSize: "10485760", StorageClass: api.StorageClassLocal, NodeName: node}
		r.gaon.SetSnapshot(s)
	}
	r.gaon.SetVolume(restoredVolume("restored", "backup"))
	r.gaon.SetVolume(volume("plain", node))
	r.run(t)

	eventually(t, "restored is Available", r.phase("restored", api.VolumeAvailable))
	eventually(t, "plain is Available", r.phase("plain", api.VolumeAvailable))

	made := map[string]*v1alpha2.CreateDiskRequest{}
	for _, req := range r.mvmd.CreateRequests() {
		made[req.GetDiskId()] = req
	}
	require.Contains(t, made, "volume-restored")
	assert.Equal(t, "snapshot-"+uid, made["volume-restored"].GetFromSnapshotId())
	assert.EqualValues(t, 10<<20, made["volume-restored"].GetSizeBytes())
	require.Contains(t, made, "volume-plain")
	assert.Empty(t, made["volume-plain"].GetFromSnapshotId(), "control: a Volume with no data source is made from no snapshot")
	disk, ok := r.mvmd.Disk("volume-restored")
	require.True(t, ok)
	assert.Empty(t, disk.GetHolderMachineId(), "no MicroVM uses the disk")
	v := r.volume(t, "restored")
	assert.Equal(t, api.Quantity("10485760"), v.Status.Capacity)
	assert.Nil(t, v.Status.Attachment)
}

func TestLocal_RunMakesNoEmptyDiskForAVolumeWhoseSnapshotIsGone(t *testing.T) {
	r := newRig(t)
	s := snapshotOf("backup", "source", uid)
	s.Status = api.VolumeSnapshotStatus{ReadyToUse: true, RestoreSize: "10485760", StorageClass: api.StorageClassLocal, NodeName: node}
	r.gaon.SetSnapshot(s)
	r.gaon.SetVolume(restoredVolume("a-lost", "absent"))
	r.gaon.SetVolume(restoredVolume("z-kept", "backup"))
	r.run(t)

	eventually(t, "a-lost is Failed", r.phase("a-lost", api.VolumeFailed))
	eventually(t, "z-kept is Available", r.phase("z-kept", api.VolumeAvailable))

	_, ok := r.mvmd.Disk("volume-a-lost")
	assert.False(t, ok, "a Volume of a snapshot that is gone gets no empty disk")
	_, ok = r.mvmd.Disk("volume-z-kept")
	assert.True(t, ok, "control: the Volume whose snapshot exists gets its disk")
}

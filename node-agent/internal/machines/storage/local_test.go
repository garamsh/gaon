package storage_test

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
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/gaon/node-agent/internal/machines/network"
	"github.com/garamsh/gaon/node-agent/internal/machines/storage"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const node = "n1"

var image = "registry.example/app@sha256:" + strings.Repeat("a", 64)

type rig struct {
	gaon  *fakegaon.Server
	mvmd  *fakemvmd.Server
	rt    v1alpha2.RuntimeServiceClient
	local *storage.Local
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := clock.System{}
	r := &rig{gaon: fakegaon.New(t), mvmd: fakemvmd.New(t, clk)}
	conn, err := runtimeapi.Dial(context.Background(), r.mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	r.rt = v1alpha2.NewRuntimeServiceClient(conn)
	r.local = storage.NewLocal(node, 10*time.Millisecond, api.NewClient(r.gaon.URL, nil), r.rt, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func machineUsing(name string, epoch api.Epoch, volumes ...string) api.Machine {
	m := api.Machine{
		Metadata: api.ObjectMeta{Name: name, Generation: 1},
		Spec: api.MachineSpec{
			Image:        image,
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "2", Memory: "1Gi"}},
			RunStrategy:  api.RunAlways,
			NodeName:     node,
			BindingEpoch: epoch,
		},
	}
	for _, v := range volumes {
		m.Spec.Volumes = append(m.Spec.Volumes, api.MachineVolume{Name: v, MountPath: "/mnt/" + v})
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

func refusal(t *testing.T, err error) *machines.Refusal {
	t.Helper()
	var ref *machines.Refusal
	require.ErrorAs(t, err, &ref)
	return ref
}

func TestLocal_AttachMakesTheDiskAndBindsTheVolumeToTheMachine(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	r.gaon.SetVolume(volume("other", node))
	m := machineUsing("db", 1, "data")
	r.gaon.SetMachine(m)

	disks, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	require.Len(t, disks, 1)
	assert.Equal(t, "volume-data", disks[0].GetDiskId())
	disk, ok := r.mvmd.Disk("volume-data")
	require.True(t, ok)
	assert.EqualValues(t, 10<<20, disk.GetSizeBytes())
	v := r.volume(t, "data")
	assert.Equal(t, api.VolumeBound, v.Status.Phase)
	assert.Equal(t, &api.Attachment{MachineName: "db", BindingEpoch: 1}, v.Status.Attachment)
	assert.Equal(t, api.Quantity("10485760"), v.Status.Capacity)
	_, ok = r.mvmd.Disk("volume-other")
	assert.False(t, ok, "control: a Volume the Machine does not name gets no disk")
	assert.Equal(t, api.VolumePending, r.volume(t, "other").Status.Phase)
}

func TestLocal_AttachIsRepeatableForTheSameMachineAndEpoch(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	m := machineUsing("db", 1, "data", "data")
	r.gaon.SetMachine(m)

	first, err := r.local.Attach(context.Background(), &m)
	require.NoError(t, err)
	// A Run round between Attach and the create reads the disk as unheld.
	v := r.volume(t, "data")
	v.Status.Phase = api.VolumeReleased
	r.gaon.SetVolume(v)
	second, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	assert.Len(t, first, 1, "a Volume named twice is one disk")
	assert.Equal(t, first[0].GetDiskId(), second[0].GetDiskId())
	assert.Equal(t, []string{"CreateDisk volume-data"}, r.mvmd.Calls(), "the disk is made once")
	assert.Equal(t, api.VolumeBound, r.volume(t, "data").Status.Phase, "the second call writes Bound again")
	assert.Equal(t, &api.Attachment{MachineName: "db", BindingEpoch: 1}, r.volume(t, "data").Status.Attachment)
}

func TestLocal_AttachRefusesAVolumeItCannotServeAndServesTheOnesItCan(t *testing.T) {
	deleting := volume("deleting", node)
	now := time.Now()
	deleting.Metadata.DeletionTimestamp = &now
	other := volume("other-class", node)
	other.Spec.StorageClass = "ceph"
	inUse := volume("in-use", node)
	inUse.Status = api.VolumeStatus{Phase: api.VolumeBound, Attachment: &api.Attachment{MachineName: "web", BindingEpoch: 1}}
	tests := []struct {
		name   string
		volume string
		seed   []api.Volume
		reason string
	}{
		{"a missing Volume", "missing", nil, storage.ReasonVolumeNotFound},
		{"a Volume of another class", "other-class", []api.Volume{other}, storage.ReasonUnsupportedStorageClass},
		{"a Volume being deleted", "deleting", []api.Volume{deleting}, storage.ReasonVolumeNotUsable},
		{"a Volume pinned to another Node", "elsewhere", []api.Volume{volume("elsewhere", "n2")}, storage.ReasonVolumeNotUsable},
		{"a Volume attached to another Machine at the same epoch", "in-use", []api.Volume{inUse}, storage.ReasonVolumeInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.gaon.SetVolume(volume("good", node))
			for _, v := range tt.seed {
				r.gaon.SetVolume(v)
			}
			m := machineUsing("db", 1, "good", tt.volume)
			r.gaon.SetMachine(m)

			_, err := r.local.Attach(context.Background(), &m)

			assert.Equal(t, tt.reason, refusal(t, err).Reason)
			_, made := r.mvmd.Disk("volume-" + tt.volume)
			assert.False(t, made, "no disk is made for the refused Volume")

			control := machineUsing("web", 1, "good")
			r.gaon.SetMachine(control)
			disks, err := r.local.Attach(context.Background(), &control)
			require.NoError(t, err, "control: a Machine naming only the good Volume is served")
			assert.Equal(t, "volume-good", disks[0].GetDiskId())
		})
	}
}

func TestLocal_AttachTakesAVolumeAttachedAtALowerEpoch(t *testing.T) {
	r := newRig(t)
	v := volume("data", node)
	v.Status = api.VolumeStatus{Phase: api.VolumeReleased, Attachment: &api.Attachment{MachineName: "db", BindingEpoch: 1}}
	r.gaon.SetVolume(v)
	r.mvmd.AddDisk("volume-data", 10<<20)
	m := machineUsing("db", 2, "data")
	r.gaon.SetMachine(m)

	_, err := r.local.Attach(context.Background(), &m)

	require.NoError(t, err)
	assert.Equal(t, &api.Attachment{MachineName: "db", BindingEpoch: 2}, r.volume(t, "data").Status.Attachment)
	assert.NotContains(t, r.mvmd.Calls(), "CreateDisk volume-data", "control: the disk that exists is not made again")
}

func TestLocal_AttachWritesFailedAndRefusesWhenTheDiskCannotBeMade(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	m := machineUsing("db", 1, "data")
	r.gaon.SetMachine(m)
	r.mvmd.FailDiskCreates(true)

	_, err := r.local.Attach(context.Background(), &m)

	assert.Equal(t, storage.ReasonVolumeFailed, refusal(t, err).Reason)
	assert.Equal(t, api.VolumeFailed, r.volume(t, "data").Status.Phase)
	assert.Nil(t, r.volume(t, "data").Status.Attachment)

	r.mvmd.FailDiskCreates(false)
	_, err = r.local.Attach(context.Background(), &m)
	require.NoError(t, err, "control: the same Machine is served once the disk can be made")
	assert.Equal(t, api.VolumeBound, r.volume(t, "data").Status.Phase)
}

func TestLocal_AttachWritesFailedOnlyForTheOutcomeKindsThatRefuseTheDisk(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	m := machineUsing("db", 1, "data")
	r.gaon.SetMachine(m)

	r.mvmd.FailDiskCreatesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED)
	_, err := r.local.Attach(context.Background(), &m)
	assert.Equal(t, storage.ReasonVolumeFailed, refusal(t, err).Reason)
	assert.Equal(t, api.VolumeFailed, r.volume(t, "data").Status.Phase)

	// Control: a kind outside REJECTED, PRECONDITION_FAILED and FAILED is
	// retried, not written as Failed.
	r.gaon.SetVolume(volume("data", node))
	r.mvmd.FailDiskCreatesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSUPPORTED)
	_, err = r.local.Attach(context.Background(), &m)
	require.Error(t, err)
	var ref *machines.Refusal
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
	m := machineUsing("db", 1, "data", "whole")
	r.gaon.SetMachine(m)

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

func TestLocal_RunWritesBoundWhileAMachineHoldsTheDiskAndReleasedKeepingTheAttachmentAfter(t *testing.T) {
	r := newRig(t)
	v := volume("data", node)
	attachment := &api.Attachment{MachineName: "db", BindingEpoch: 1}
	v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Attachment: attachment, Capacity: "10485760"}
	r.gaon.SetVolume(v)
	r.mvmd.AddDisk("volume-data", 10<<20)
	_, err := r.mvmd.CreateMachine(context.Background(), &v1alpha2.CreateMachineRequest{MachineId: "db-e1", Disks: []*v1alpha2.DiskAttachment{{DiskId: "volume-data"}}})
	require.NoError(t, err)
	r.run(t)

	eventually(t, "data is Bound", r.phase("data", api.VolumeBound))
	assert.Equal(t, attachment, r.volume(t, "data").Status.Attachment)

	_, err = r.mvmd.DeleteMachine(context.Background(), &v1alpha2.DeleteMachineRequest{MachineId: "db-e1"})
	require.NoError(t, err)
	eventually(t, "data is Released", r.phase("data", api.VolumeReleased))
	assert.Equal(t, attachment, r.volume(t, "data").Status.Attachment, "Released keeps the attachment")
}

func TestLocal_RunDeletesTheDiskAndRemovesOnlyItsFinalizerOfAVolumeNoMachineHolds(t *testing.T) {
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
	_, err := r.mvmd.CreateMachine(context.Background(), &v1alpha2.CreateMachineRequest{MachineId: "db-e1", Disks: []*v1alpha2.DiskAttachment{{DiskId: "volume-a-held"}}})
	require.NoError(t, err)
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
	_, err := r.mvmd.CreateMachine(context.Background(), &v1alpha2.CreateMachineRequest{MachineId: "db-e1", Disks: []*v1alpha2.DiskAttachment{{DiskId: "volume-a-held"}}})
	require.NoError(t, err)
	r.run(t)

	eventually(t, "b-free is gone", func() bool { _, ok := r.gaon.Volume("b-free"); return !ok })
	_, ok := r.mvmd.Disk("volume-a-held")
	assert.True(t, ok, "control: the disk of a held Volume is kept")
	_, exists := r.gaon.Volume("a-held")
	assert.True(t, exists, "control: the held Volume stays")

	_, err = r.mvmd.DeleteMachine(context.Background(), &v1alpha2.DeleteMachineRequest{MachineId: "db-e1"})
	require.NoError(t, err)
	eventually(t, "a-held is gone", func() bool { _, ok := r.gaon.Volume("a-held"); return !ok })
	_, ok = r.mvmd.Disk("volume-a-held")
	assert.False(t, ok)
}

func TestLocal_ServesAMachineThroughTheAgentFromCreateToDelete(t *testing.T) {
	r := newRig(t)
	r.mvmd.Arm(time.Hour, 1)
	r.gaon.SetVolume(volume("data", node))
	clk := clock.System{}
	def, err := network.NewDefault(nil, nil)
	require.NoError(t, err)
	svc := machines.NewService(node, machines.Params{
		Resync: 20 * time.Millisecond, Retry: 20 * time.Millisecond, ReadTimeout: time.Second,
		RestartBackoff: time.Hour, RestartBackoffMax: time.Hour, RestartResetAfter: time.Hour,
	}, api.NewClient(r.gaon.URL, nil), r.rt, def, r.local, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	r.run(t)
	r.gaon.SetMachine(machineUsing("db", 1, "data"))

	eventually(t, "db runs with the disk", func() bool {
		m, ok := r.mvmd.Machine("db-e1")
		return ok && m.GetState() == v1alpha2.MachineState_MACHINE_STATE_RUNNING && len(m.GetDisks()) == 1
	})
	eventually(t, "data is Bound to db", func() bool {
		v := r.volume(t, "data")
		return v.Status.Phase == api.VolumeBound && v.Status.Attachment != nil && v.Status.Attachment.MachineName == "db"
	})

	r.gaon.DeleteMachine("db")
	eventually(t, "data is Released once db is gone", r.phase("data", api.VolumeReleased))
	assert.Equal(t, &api.Attachment{MachineName: "db", BindingEpoch: 1}, r.volume(t, "data").Status.Attachment)
	_, ok := r.mvmd.Disk("volume-data")
	assert.True(t, ok, "the disk outlives the Machine")
}

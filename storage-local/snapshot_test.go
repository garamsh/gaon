package storagelocal_test

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const uid = "0123456789abcdef0123456789abcdef"

func snapshotOf(name, volumeName string, id api.UID) api.VolumeSnapshot {
	return api.VolumeSnapshot{
		Metadata: api.ObjectMeta{Name: name, UID: id, Generation: 1},
		Spec:     api.VolumeSnapshotSpec{VolumeName: volumeName},
	}
}

// diskedVolume is a Volume pinned to this Node whose 10Mi disk exists.
func diskedVolume(r *rig, name string) api.Volume {
	v := volume(name, node)
	v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
	r.mvmd.AddDisk("volume-"+name, 10<<20)
	r.gaon.SetVolume(v)
	return v
}

func snapshottingOf(s api.VolumeSnapshot) *api.Condition {
	return findCondition(s.Status.Conditions, api.VolumeSnapshotConditionSnapshotting)
}

func findCondition(conditions []api.Condition, typ string) *api.Condition {
	i := slices.IndexFunc(conditions, func(c api.Condition) bool { return c.Type == typ })
	if i < 0 {
		return nil
	}
	return &conditions[i]
}

func (r *rig) snapshot(t *testing.T, name string) api.VolumeSnapshot {
	t.Helper()
	s, ok := r.gaon.Snapshot(name)
	require.True(t, ok, "snapshot %s exists", name)
	return s
}

func (r *rig) ready(name string) func() bool {
	return func() bool {
		s, ok := r.gaon.Snapshot(name)
		return ok && s.Status.ReadyToUse
	}
}

func (r *rig) gone(name string) func() bool {
	return func() bool {
		_, ok := r.gaon.Snapshot(name)
		return !ok
	}
}

func callsWith(r *rig, prefix string) []string {
	return slices.DeleteFunc(r.mvmd.Calls(), func(c string) bool { return !strings.HasPrefix(c, prefix) })
}

func TestLocal_RunTakesTheSnapshotUnderItsFinalizerAndWritesItReadyWithTheSizeInOneWrite(t *testing.T) {
	r := newRig(t)
	diskedVolume(r, "data")
	s := snapshotOf("backup", "data", uid)
	s.Metadata.Finalizers = []string{"example.com/other"}
	s.Status.Conditions = []api.Condition{{Type: "Other", Status: api.ConditionTrue, Reason: "Kept"}}
	r.gaon.SetSnapshot(s)
	var mu sync.Mutex
	var atCall *api.VolumeSnapshot
	r.mvmd.OnSnapshot(func() {
		mu.Lock()
		defer mu.Unlock()
		got, _ := r.gaon.Snapshot("backup")
		atCall = &got
	})
	r.run(t)

	eventually(t, "the snapshot is ready", r.ready("backup"))

	mu.Lock()
	require.NotNil(t, atCall)
	assert.Equal(t, []string{"example.com/other", api.NodeAgentFinalizer}, atCall.Metadata.Finalizers, "the finalizer is there before the first call and the other one is kept")
	require.NotNil(t, snapshottingOf(*atCall))
	assert.Equal(t, "InProgress", snapshottingOf(*atCall).Reason, "InProgress is written before the call")
	mu.Unlock()
	got := r.snapshot(t, "backup")
	assert.Equal(t, api.Quantity("10485760"), got.Status.RestoreSize)
	assert.Equal(t, api.StorageClassLocal, got.Status.StorageClass)
	assert.Equal(t, node, got.Status.NodeName)
	assert.Nil(t, snapshottingOf(got), "Snapshotting goes with readyToUse")
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, "Other", got.Status.Conditions[0].Type, "control: another condition stays")
	for _, st := range r.gaon.SnapshotStatuses() {
		assert.Equal(t, !st.ReadyToUse, findCondition(st.Conditions, api.VolumeSnapshotConditionSnapshotting) != nil, "Snapshotting is removed in the write that sets readyToUse: %+v", st)
	}
	assert.Equal(t, []string{"SnapshotDisk volume-data snapshot-" + uid + " take-snapshot-" + uid}, callsWith(r, "SnapshotDisk"))
	disk, ok := r.mvmd.DiskSnapshot("snapshot-" + uid)
	require.True(t, ok)
	assert.Equal(t, "volume-data", disk.GetDiskId())
}

func TestLocal_RunHoldsASnapshotWhoseSourceHasNoDiskAndTakesItOnceItHas(t *testing.T) {
	r := newRig(t)
	r.gaon.SetVolume(volume("data", node))
	r.gaon.SetSnapshot(snapshotOf("backup", "data", uid))
	// No disk can be made, so the Volume has no capacity for as long as this holds.
	r.mvmd.FailDiskCreates(true)
	r.run(t)

	eventually(t, "SourceNotReady", func() bool {
		s, _ := r.gaon.Snapshot("backup")
		c := snapshottingOf(s)
		return c != nil && c.Reason == "SourceNotReady"
	})

	held := r.snapshot(t, "backup")
	assert.False(t, held.Status.ReadyToUse)
	assert.Empty(t, held.Metadata.Finalizers, "no snapshot file can exist, so no finalizer yet")
	assert.Empty(t, callsWith(r, "SnapshotDisk"))

	// The control: the disk can be made, the Volume gets its capacity, and the
	// same snapshot is taken.
	r.mvmd.FailDiskCreates(false)
	eventually(t, "the snapshot is ready once its source has a disk", r.ready("backup"))
	assert.Nil(t, snapshottingOf(r.snapshot(t, "backup")))
}

func TestLocal_RunLeavesASnapshotNobodyOnThisNodeTakesNotReadyWithNoCondition(t *testing.T) {
	tests := []struct {
		name  string
		setup func(r *rig)
	}{
		{"no source volume", func(r *rig) {}},
		{"unpinned source", func(r *rig) {
			v := volume("data", "")
			v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
			r.gaon.SetVolume(v)
		}},
		{"source of another class", func(r *rig) {
			v := volume("data", node)
			v.Spec.StorageClass = "fast"
			v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
			r.gaon.SetVolume(v)
			r.mvmd.AddDisk("volume-data", 10<<20)
		}},
		{"source pinned to another node", func(r *rig) {
			v := volume("data", "n2")
			v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
			r.gaon.SetVolume(v)
			r.mvmd.AddDisk("volume-data", 10<<20)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			tt.setup(r)
			// "a-" sorts before "z-", so the round that readies the control has
			// already passed the snapshot under test.
			r.gaon.SetSnapshot(snapshotOf("a-nobody", "data", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
			diskedVolume(r, "mine")
			r.gaon.SetSnapshot(snapshotOf("z-control", "mine", "cccccccccccccccccccccccccccccccc"))
			r.run(t)

			eventually(t, "the control is ready", r.ready("z-control"))

			got := r.snapshot(t, "a-nobody")
			assert.False(t, got.Status.ReadyToUse)
			assert.Empty(t, got.Metadata.Finalizers)
			assert.Empty(t, got.Status.Conditions)
			assert.Equal(t, []string{"SnapshotDisk volume-mine snapshot-cccccccccccccccccccccccccccccccc take-snapshot-cccccccccccccccccccccccccccccccc"}, callsWith(r, "SnapshotDisk"), "control: only the snapshot of this Node's Volume was taken")
		})
	}
}

func TestLocal_RunShowsWhyAnAnswerFromMvmdHoldsTheSnapshotAndRetriesWithTheSameIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		kind   v1alpha2.OutcomeKind
		reason string
	}{
		{"holder not snapshottable", v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "HolderNotSnapshottable"},
		{"any other answer", v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED, "Failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			diskedVolume(r, "data")
			r.gaon.SetSnapshot(snapshotOf("backup", "data", uid))
			r.mvmd.FailDiskSnapshotsWith(tt.kind)
			r.run(t)

			eventually(t, tt.reason, func() bool {
				s, _ := r.gaon.Snapshot("backup")
				c := snapshottingOf(s)
				return c != nil && c.Reason == tt.reason && len(callsWith(r, "SnapshotDisk")) >= 2
			})

			held := r.snapshot(t, "backup")
			assert.False(t, held.Status.ReadyToUse)
			c := snapshottingOf(held)
			assert.Equal(t, api.ConditionTrue, c.Status)
			assert.Equal(t, "mvmd refuses the snapshot", c.Message, "mvmd's message")
			assert.Equal(t, []string{api.NodeAgentFinalizer}, held.Metadata.Finalizers)

			// The control: the same snapshot, once mvmd answers.
			r.mvmd.FailDiskSnapshotsWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED)
			eventually(t, "the snapshot is ready", r.ready("backup"))
			assert.Nil(t, snapshottingOf(r.snapshot(t, "backup")))
			calls := callsWith(r, "SnapshotDisk")
			assert.Equal(t, 1, len(slices.Compact(slices.Clone(calls))), "every attempt carries the same disk, snapshot and request identifiers: %v", calls)
		})
	}
}

func TestLocal_RunLeavesAReadySnapshotAlone(t *testing.T) {
	r := newRig(t)
	diskedVolume(r, "data")
	s := snapshotOf("a-backup", "data", uid)
	s.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	s.Status = api.VolumeSnapshotStatus{ReadyToUse: true, RestoreSize: "5", StorageClass: api.StorageClassLocal, NodeName: node}
	r.gaon.SetSnapshot(s)
	r.gaon.SetSnapshot(snapshotOf("z-control", "data", "cccccccccccccccccccccccccccccccc"))
	r.run(t)

	eventually(t, "the control is ready", r.ready("z-control"))

	got := r.snapshot(t, "a-backup")
	assert.Equal(t, api.Quantity("5"), got.Status.RestoreSize)
	assert.Equal(t, []string{"SnapshotDisk volume-data snapshot-cccccccccccccccccccccccccccccccc take-snapshot-cccccccccccccccccccccccccccccccc"}, callsWith(r, "SnapshotDisk"), "control: the one not ready was taken, the ready one not again")
}

// readySnapshot is a ready snapshot of this Node held under the agent's
// finalizer, with its disk snapshot in mvmd.
func readySnapshot(r *rig, name string, id api.UID, volumeName string) {
	s := snapshotOf(name, volumeName, id)
	s.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	s.Status = api.VolumeSnapshotStatus{ReadyToUse: true, RestoreSize: "10485760", StorageClass: api.StorageClassLocal, NodeName: node}
	r.gaon.SetSnapshot(s)
	r.mvmd.AddSnapshot("snapshot-"+string(id), "volume-"+volumeName, 10<<20)
}

func TestLocal_RunDeletesTheDiskSnapshotAndRemovesTheFinalizerOnDelete(t *testing.T) {
	r := newRig(t)
	readySnapshot(r, "backup", uid, "data")
	readySnapshot(r, "other", "cccccccccccccccccccccccccccccccc", "data")
	mine := r.snapshot(t, "backup")
	mine.Metadata.Finalizers = []string{"example.com/first", api.NodeAgentFinalizer, "example.com/last"}
	r.gaon.SetSnapshot(mine)
	r.gaon.MarkSnapshotDeleting("backup")
	r.run(t)

	eventually(t, "the agent's finalizer is removed", func() bool {
		s, _ := r.gaon.Snapshot("backup")
		return !slices.Contains(s.Metadata.Finalizers, api.NodeAgentFinalizer)
	})

	assert.Equal(t, []string{"example.com/first", "example.com/last"}, r.snapshot(t, "backup").Metadata.Finalizers, "no other entry is changed")
	_, ok := r.mvmd.DiskSnapshot("snapshot-" + uid)
	assert.False(t, ok, "mvmd's snapshot is deleted")
	assert.Equal(t, []string{"DeleteDiskSnapshot snapshot-" + uid}, callsWith(r, "DeleteDiskSnapshot"))
	_, ok = r.mvmd.DiskSnapshot("snapshot-cccccccccccccccccccccccccccccccc")
	assert.True(t, ok, "control: a snapshot not being deleted keeps its disk snapshot")
	assert.Contains(t, r.snapshot(t, "other").Metadata.Finalizers, api.NodeAgentFinalizer)
}

func TestLocal_RunKeepsTheSnapshotWhileAVolumeMadeFromItHasNoDisk(t *testing.T) {
	r := newRig(t)
	// "a-" sorts before "z-", so the round that removes the control has already
	// passed the snapshot that is held.
	readySnapshot(r, "a-held", uid, "data")
	readySnapshot(r, "z-free", "cccccccccccccccccccccccccccccccc", "data")
	r.gaon.MarkSnapshotDeleting("a-held")
	r.gaon.MarkSnapshotDeleting("z-free")
	waiting := volume("restored", node)
	waiting.Spec.DataSource = "a-held"
	r.gaon.SetVolume(waiting)
	r.mvmd.FailDiskCreates(true)
	r.run(t)

	eventually(t, "the control is removed", r.gone("z-free"))

	held := r.snapshot(t, "a-held")
	assert.Equal(t, []string{api.NodeAgentFinalizer}, held.Metadata.Finalizers)
	_, ok := r.mvmd.DiskSnapshot("snapshot-" + uid)
	assert.True(t, ok, "the disk snapshot the Volume needs is kept")
	assert.Empty(t, callsWith(r, "DeleteDiskSnapshot snapshot-"+uid))

	// The control: the Volume's disk exists, so it no longer needs the snapshot.
	v := r.volume(t, "restored")
	v.Status.Capacity = "10485760"
	r.gaon.SetVolume(v)
	eventually(t, "the snapshot is removed once the Volume has capacity", r.gone("a-held"))
	_, ok = r.mvmd.DiskSnapshot("snapshot-" + uid)
	assert.False(t, ok)
}

func TestLocal_RunDoesNotWaitForAVolumeThatIsBeingDeleted(t *testing.T) {
	r := newRig(t)
	readySnapshot(r, "backup", uid, "data")
	r.gaon.MarkSnapshotDeleting("backup")
	waiting := volume("restored", node)
	waiting.Spec.DataSource = "backup"
	// Another finalizer keeps the Volume, and so its deletion stays in sight.
	waiting.Metadata.Finalizers = append(waiting.Metadata.Finalizers, "example.com/other")
	r.gaon.SetVolume(waiting)
	r.gaon.MarkVolumeDeleting("restored")
	r.run(t)

	eventually(t, "the snapshot is removed", r.gone("backup"))
}

func TestLocal_RunRetriesADiskSnapshotMvmdIsUsingAndKeepsTheFinalizer(t *testing.T) {
	r := newRig(t)
	readySnapshot(r, "backup", uid, "data")
	r.gaon.MarkSnapshotDeleting("backup")
	r.mvmd.FailSnapshotDeletesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED)
	r.run(t)

	eventually(t, "the delete is retried", func() bool { return len(callsWith(r, "DeleteDiskSnapshot")) >= 2 })

	assert.Equal(t, []string{api.NodeAgentFinalizer}, r.snapshot(t, "backup").Metadata.Finalizers)

	// The control: the same snapshot, once mvmd no longer uses its disk snapshot.
	r.mvmd.FailSnapshotDeletesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED)
	eventually(t, "the snapshot is removed", r.gone("backup"))
}

func TestLocal_RunLeavesTheDeletionOfAnotherNodesSnapshotAlone(t *testing.T) {
	r := newRig(t)
	readySnapshot(r, "a-theirs", uid, "data")
	theirs := r.snapshot(t, "a-theirs")
	theirs.Status.NodeName = "n2"
	r.gaon.SetSnapshot(theirs)
	readySnapshot(r, "z-mine", "cccccccccccccccccccccccccccccccc", "data")
	r.gaon.MarkSnapshotDeleting("a-theirs")
	r.gaon.MarkSnapshotDeleting("z-mine")
	r.run(t)

	eventually(t, "the control is removed", r.gone("z-mine"))

	assert.Equal(t, []string{api.NodeAgentFinalizer}, r.snapshot(t, "a-theirs").Metadata.Finalizers)
	assert.Equal(t, []string{"DeleteDiskSnapshot snapshot-cccccccccccccccccccccccccccccccc"}, callsWith(r, "DeleteDiskSnapshot"))
}

func TestLocal_RunDeletesASnapshotThatNeverFinishedFromItsSourcesNode(t *testing.T) {
	r := newRig(t)
	diskedVolume(r, "data")
	s := snapshotOf("backup", "data", uid)
	s.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	r.gaon.SetSnapshot(s)
	r.gaon.MarkSnapshotDeleting("backup")
	r.mvmd.AddSnapshot("snapshot-"+uid, "volume-data", 10<<20)
	r.run(t)

	eventually(t, "the snapshot is removed", r.gone("backup"))

	_, ok := r.mvmd.DiskSnapshot("snapshot-" + uid)
	assert.False(t, ok)
	assert.Empty(t, callsWith(r, "SnapshotDisk"), "a snapshot being deleted is not taken")
}

func TestLocal_RunKeepsTheDiskOfADeletingVolumeWhileASnapshotOfItIsUnfinished(t *testing.T) {
	tests := []struct {
		name     string
		snapshot func(s *api.VolumeSnapshot)
		held     bool
	}{
		{"not ready", func(s *api.VolumeSnapshot) {}, true},
		{"ready", func(s *api.VolumeSnapshot) {
			s.Status = api.VolumeSnapshotStatus{ReadyToUse: true, RestoreSize: "10485760", StorageClass: api.StorageClassLocal, NodeName: node}
		}, false},
		{"not ready and being deleted", func(s *api.VolumeSnapshot) {
			// Another finalizer keeps it, so it stays not ready and deleting.
			s.Metadata.Finalizers = []string{"example.com/other"}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			// "a-" sorts before "z-", so the round that frees the control has
			// already passed the Volume under test.
			diskedVolume(r, "a-data")
			diskedVolume(r, "z-free")
			s := snapshotOf("backup", "a-data", uid)
			tt.snapshot(&s)
			r.gaon.SetSnapshot(s)
			if tt.name == "not ready and being deleted" {
				r.gaon.MarkSnapshotDeleting("backup")
			}
			// mvmd refuses the snapshot, so a snapshot not ready stays so.
			r.mvmd.FailDiskSnapshotsWith(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED)
			r.gaon.MarkVolumeDeleting("a-data")
			r.gaon.MarkVolumeDeleting("z-free")
			r.run(t)

			eventually(t, "the control is deleted", func() bool { _, ok := r.gaon.Volume("z-free"); return !ok })

			if !tt.held {
				eventually(t, "the Volume is deleted", func() bool { _, ok := r.gaon.Volume("a-data"); return !ok })
				_, ok := r.mvmd.Disk("volume-a-data")
				assert.False(t, ok)
				return
			}
			v := r.volume(t, "a-data")
			assert.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers)
			_, ok := r.mvmd.Disk("volume-a-data")
			assert.True(t, ok, "the disk is kept")
			assert.Empty(t, callsWith(r, "DeleteDisk volume-a-data"))

			// The control: the same Volume once the snapshot is ready.
			r.mvmd.FailDiskSnapshotsWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED)
			eventually(t, "the Volume is deleted once the snapshot is ready", func() bool { _, ok := r.gaon.Volume("a-data"); return !ok })
			_, ok = r.mvmd.Disk("volume-a-data")
			assert.False(t, ok)
			assert.True(t, r.snapshot(t, "backup").Status.ReadyToUse)
		})
	}
}

func TestLocal_RunKeepsTheDiskOfADeletingVolumeWhileASnapshotOfItHoldsTheAgentsFinalizerWithNoNode(t *testing.T) {
	r := newRig(t)
	// "a-" sorts before "z-", so the round that frees the control has already
	// passed the Volume under test.
	diskedVolume(r, "a-data")
	diskedVolume(r, "z-free")
	s := snapshotOf("backup", "a-data", uid)
	s.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	r.gaon.SetSnapshot(s)
	r.mvmd.AddSnapshot("snapshot-"+uid, "volume-a-data", 10<<20)
	r.gaon.MarkSnapshotDeleting("backup")
	// mvmd refuses to delete the disk snapshot, so the finalizer stays.
	r.mvmd.FailSnapshotDeletesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED)
	r.gaon.MarkVolumeDeleting("a-data")
	r.gaon.MarkVolumeDeleting("z-free")
	r.run(t)

	eventually(t, "the control is deleted", func() bool { _, ok := r.gaon.Volume("z-free"); return !ok })
	eventually(t, "the delete is retried", func() bool { return len(callsWith(r, "DeleteDiskSnapshot")) >= 2 })

	v := r.volume(t, "a-data")
	assert.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers)
	_, ok := r.mvmd.Disk("volume-a-data")
	assert.True(t, ok, "the disk is kept")

	// The control: the same snapshot once its finalizer is gone.
	r.mvmd.FailSnapshotDeletesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED)
	eventually(t, "the Volume is deleted once the snapshot is", func() bool { _, ok := r.gaon.Volume("a-data"); return !ok })
	assert.True(t, r.gone("backup")())
	_, ok = r.mvmd.Disk("volume-a-data")
	assert.False(t, ok)
}

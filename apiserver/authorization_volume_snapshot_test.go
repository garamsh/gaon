package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// snapshotOf creates a snapshot of the Volume v, with the Node Agent's
// finalizer, as the administrator.
func (f *nodeFixture) snapshotOf(t *testing.T, name, v string) *api.VolumeSnapshot {
	t.Helper()
	s := volumeSnapshot(name, v)
	s.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	created, err := f.admin.PutVolumeSnapshot(ctx(t), s)
	require.NoError(t, err)
	return created
}

func TestNodeAuthorizer_ANodeReadsVolumeSnapshotsAndAnyoneElseDoesNot(t *testing.T) {
	f := newNodeFixture(t)
	f.snapshotOf(t, "s1", "v1")
	stranger, _ := f.c.clients(t, f.addr, strangerID)

	_, err := stranger.GetVolumeSnapshot(ctx(t), "s1")
	assert.ErrorIs(t, err, api.ErrForbidden, "a caller that is no Node")
	_, err = stranger.ListVolumeSnapshots(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = stranger.WatchVolumeSnapshots(ctx(t), "1")
	assert.ErrorIs(t, err, api.ErrForbidden)

	_, err = f.node2.GetVolumeSnapshot(ctx(t), "s1")
	assert.NoError(t, err, "control: a Node reads a snapshot of a Volume pinned to another Node")
	list, err := f.node1.ListVolumeSnapshots(ctx(t))
	require.NoError(t, err, "control: a Node lists snapshots")
	assert.Len(t, list.Items, 1)
	w, err := f.node1.WatchVolumeSnapshots(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err, "control: a Node watches snapshots")
	w.Close()
}

func TestNodeAuthorizer_ANodeNeitherCreatesNorDeletesAVolumeSnapshot(t *testing.T) {
	f := newNodeFixture(t)
	s := f.snapshotOf(t, "s1", "v1")

	_, err := f.node1.PutVolumeSnapshot(ctx(t), volumeSnapshot("fresh", "v1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.DeleteVolumeSnapshot(ctx(t), "s1", s.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden)

	_, err = f.admin.PutVolumeSnapshot(ctx(t), volumeSnapshot("fresh", "v1"))
	assert.NoError(t, err, "control: an administrator creates one")
	_, err = f.admin.DeleteVolumeSnapshot(ctx(t), "s1", s.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: an administrator deletes one")
}

func TestNodeAuthorizer_ANodeWritesTheStatusOfAVolumeSnapshotItsStatusOrItsSourceVolumeNamesIt(t *testing.T) {
	f := newNodeFixture(t)

	// Source Volume v1 is pinned to node-1, and the status names no Node.
	pinned := f.snapshotOf(t, "pinned", "v1")
	pinned.Status = readyStatus("node-1")
	_, err := f.node2.PutVolumeSnapshotStatus(ctx(t), pinned)
	assert.ErrorIs(t, err, api.ErrForbidden, "the source Volume is pinned to another Node")
	held, err := f.node1.PutVolumeSnapshotStatus(ctx(t), pinned)
	require.NoError(t, err, "control: the source Volume is pinned to node-1")

	// Source Volume v2 is pinned to no Node: the status names node-2.
	unpinned := f.snapshotOf(t, "unpinned", "v2")
	unpinned.Status = readyStatus("node-2")
	_, err = f.node1.PutVolumeSnapshotStatus(ctx(t), unpinned)
	assert.ErrorIs(t, err, api.ErrForbidden, "neither the status nor the source Volume names node-1")
	_, err = f.node2.PutVolumeSnapshotStatus(ctx(t), unpinned)
	assert.ErrorIs(t, err, api.ErrForbidden, "a snapshot that is not yet ready names no Node")

	// A ready snapshot of a Volume pinned to no Node is held by the Node its
	// status names. The administrator writes the status that names it.
	_, err = f.admin.PutVolumeSnapshotStatus(ctx(t), unpinned)
	require.NoError(t, err)
	unpinned, err = f.admin.GetVolumeSnapshot(ctx(t), "unpinned")
	require.NoError(t, err)
	unpinned.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: "False"}}
	_, err = f.node1.PutVolumeSnapshotStatus(ctx(t), unpinned)
	assert.ErrorIs(t, err, api.ErrForbidden, "another Node than the one the status names")
	_, err = f.node2.PutVolumeSnapshotStatus(ctx(t), unpinned)
	assert.NoError(t, err, "control: the Node the status names")

	held.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: "False"}}
	_, err = f.node1.PutVolumeSnapshotStatus(ctx(t), held)
	assert.NoError(t, err, "control: the source Volume names node-1, and so does the status")

	absent := volumeSnapshot("absent", "v1")
	_, err = f.node1.PutVolumeSnapshotStatus(ctx(t), absent)
	assert.ErrorIs(t, err, api.ErrForbidden, "a snapshot that does not exist is no Node's")
}

func TestNodeAuthorizer_ANodeChangesOnlyItsFinalizerOfAVolumeSnapshotItHolds(t *testing.T) {
	f := newNodeFixture(t)
	s := f.snapshotOf(t, "s1", "v1")

	retarget := *s
	retarget.Spec.VolumeName = "v2"
	_, err := f.node1.PutVolumeSnapshot(ctx(t), &retarget)
	assert.ErrorIs(t, err, api.ErrForbidden, "a spec change")
	other := *s
	other.Metadata.Finalizers = []string{api.NodeAgentFinalizer, "example.com/other"}
	_, err = f.node1.PutVolumeSnapshot(ctx(t), &other)
	assert.ErrorIs(t, err, api.ErrForbidden, "a finalizer of another owner")
	labelled := *s
	labelled.Metadata.Labels = map[string]string{"app": "web"}
	_, err = f.node1.PutVolumeSnapshot(ctx(t), &labelled)
	assert.ErrorIs(t, err, api.ErrForbidden, "a label")
	both := *s
	both.Spec.VolumeName = "v2"
	both.Metadata.Finalizers = nil
	_, err = f.node1.PutVolumeSnapshot(ctx(t), &both)
	assert.ErrorIs(t, err, api.ErrForbidden, "its finalizer together with a spec change")
	_, err = f.node2.PutVolumeSnapshot(ctx(t), s)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node that does not hold the snapshot")

	stale := *s
	stale.Metadata.ResourceVersion = "1"
	_, err = f.node1.PutVolumeSnapshot(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version is the server's to refuse, not the authorizer's")
	removed := *s
	removed.Metadata.Finalizers = nil
	removed.Status = readyStatus("node-1")
	removed.Metadata.Generation = 9
	cleared, err := f.node1.PutVolumeSnapshot(ctx(t), &removed)
	require.NoError(t, err, "control: removing its finalizer")
	cleared.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	_, err = f.node1.PutVolumeSnapshot(ctx(t), cleared)
	assert.NoError(t, err, "control: adding its finalizer")
}

func TestNodeAuthorizer_ARoleThatGrantsUpdateOnVolumeSnapshotsLetsAnyChangeThrough(t *testing.T) {
	f := newNodeFixture(t)
	s := f.snapshotOf(t, "s1", "v1")
	other := *s
	other.Metadata.Finalizers = []string{api.NodeAgentFinalizer, "example.com/other"}
	_, err := f.node1.PutVolumeSnapshot(ctx(t), &other)
	require.ErrorIs(t, err, api.ErrForbidden)

	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("snapshot-writer", rule(api.Group, "volumesnapshots", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("snapshot-writer", "snapshot-writer", userSubject("node-1")))
	require.NoError(t, err)

	_, err = f.node1.PutVolumeSnapshot(ctx(t), &other)
	assert.NoError(t, err, "the same write once a role grants update on volumesnapshots")
}

func TestRBAC_ARoleNamingVolumeSnapshotsGrantsTheirVerbsAndNoMore(t *testing.T) {
	f := newNodeFixture(t)
	s := f.snapshotOf(t, "s1", "v1")
	reader, _ := f.c.clients(t, f.addr, strangerID)
	_, err := reader.GetVolumeSnapshot(ctx(t), "s1")
	require.ErrorIs(t, err, api.ErrForbidden)

	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("snapshot-reader", rule(api.Group, "volumesnapshots", api.VerbGet, api.VerbList, api.VerbWatch)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("snapshot-reader", "snapshot-reader", userSubject(strangerID.cn)))
	require.NoError(t, err)

	_, err = reader.GetVolumeSnapshot(ctx(t), "s1")
	assert.NoError(t, err, "the role grants get")
	_, err = reader.ListVolumeSnapshots(ctx(t))
	assert.NoError(t, err, "the role grants list")
	_, err = reader.DeleteVolumeSnapshot(ctx(t), "s1", s.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "the role does not grant delete")
	s.Status = readyStatus("node-1")
	_, err = reader.PutVolumeSnapshotStatus(ctx(t), s)
	assert.ErrorIs(t, err, api.ErrForbidden, "the role does not grant update of the status")
	_, err = f.admin.PutVolumeSnapshotStatus(ctx(t), s)
	assert.NoError(t, err, "control: an administrator writes it")
	deleted, err := f.admin.GetVolumeSnapshot(ctx(t), "s1")
	require.NoError(t, err)
	_, err = f.admin.DeleteVolumeSnapshot(ctx(t), "s1", deleted.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: an administrator deletes it")
}

func TestNodeAuthorizer_ACallerThatIsNoNodeIsNotGrantedASnapshotByTheNodeNameOfItsStatus(t *testing.T) {
	f := newNodeFixture(t)
	ghost, _ := f.c.clients(t, f.addr, nodeID("ghost"))
	s := f.snapshotOf(t, "ghosted", "v2")
	s.Status = readyStatus("ghost")
	_, err := f.admin.PutVolumeSnapshotStatus(ctx(t), s)
	require.NoError(t, err)
	s, err = f.admin.GetVolumeSnapshot(ctx(t), "ghosted")
	require.NoError(t, err)
	require.Equal(t, "ghost", s.Status.NodeName, "the status names a CN that is no Node")

	s.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: "False"}}
	_, err = ghost.PutVolumeSnapshotStatus(ctx(t), s)
	assert.ErrorIs(t, err, api.ErrForbidden, "a status write by a CN that is no Node")
	unfinalized := *s
	unfinalized.Metadata.Finalizers = nil
	_, err = ghost.PutVolumeSnapshot(ctx(t), &unfinalized)
	assert.ErrorIs(t, err, api.ErrForbidden, "its finalizer removed by a CN that is no Node")

	_, err = f.admin.PutNode(ctx(t), node("ghost"))
	require.NoError(t, err)
	_, err = ghost.PutVolumeSnapshotStatus(ctx(t), s)
	assert.NoError(t, err, "control: the same write once a Node is named ghost")
}

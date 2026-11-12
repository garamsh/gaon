package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

func TestNodeAuthorizer_ANodeReadsVolumesAndAnyoneElseDoesNot(t *testing.T) {
	f := newNodeFixture(t)
	stranger, _ := f.c.clients(t, f.addr, strangerID)

	_, err := stranger.GetVolume(ctx(t), "v1")
	assert.ErrorIs(t, err, api.ErrForbidden, "a caller that is no Node")
	_, err = stranger.ListVolumes(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = stranger.WatchVolumes(ctx(t), "1")
	assert.ErrorIs(t, err, api.ErrForbidden)

	_, err = f.node2.GetVolume(ctx(t), "v1")
	assert.NoError(t, err, "control: a Node reads a Volume pinned to another Node")
	list, err := f.node1.ListVolumes(ctx(t))
	require.NoError(t, err, "control: a Node lists Volumes")
	assert.Len(t, list.Items, 2)
	w, err := f.node1.WatchVolumes(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err, "control: a Node watches Volumes")
	w.Close()
}

func TestNodeAuthorizer_ANodeNeitherCreatesNorDeletesAVolume(t *testing.T) {
	f := newNodeFixture(t)

	_, err := f.node1.PutVolume(ctx(t), volume("fresh"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.DeleteVolume(ctx(t), "v1", f.v1.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden)

	_, err = f.admin.PutVolume(ctx(t), volume("fresh"))
	assert.NoError(t, err, "control: an administrator creates one")
}

func TestNodeAuthorizer_ANodeWritesTheStatusOfAVolumePinnedToItOrUsedByAMicroVMBoundToIt(t *testing.T) {
	f := newNodeFixture(t)

	// Used and not pinned: a class the node agent does not serve is never
	// pinned, and a MicroVM of node-1 mounts it.
	remote := volume("remote")
	remote.Spec.StorageClass = "ceph"
	remote.Spec.AccessModes = nil
	remoteV, err := f.admin.PutVolume(ctx(t), remote)
	require.NoError(t, err)
	mvm := microvm("mvm")
	mvm.Spec.Volumes = []api.MicroVMVolume{{Name: "remote", MountPath: "/remote"}}
	mvm, err = f.admin.PutMicroVM(ctx(t), mvm)
	require.NoError(t, err)
	_, err = f.admin.BindMicroVM(ctx(t), "mvm", api.Binding{ResourceVersion: mvm.Metadata.ResourceVersion, NodeName: "node-1", BindingEpoch: 1})
	require.NoError(t, err)
	remoteV.Status.Phase = api.VolumeAvailable
	_, err = f.node2.PutVolumeStatus(ctx(t), remoteV)
	assert.ErrorIs(t, err, api.ErrForbidden, "used by a MicroVM of another Node")
	remoteV, err = f.node1.PutVolumeStatus(ctx(t), remoteV)
	assert.NoError(t, err, "control: used by a MicroVM bound to node-1, pinned to none")

	// Pinned and not used: the MicroVM that used v1 is gone.
	m1, err := f.admin.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)
	_, err = f.admin.DeleteMicroVM(ctx(t), "m1", m1.Metadata.ResourceVersion)
	require.NoError(t, err)
	v1, err := f.admin.GetVolume(ctx(t), "v1")
	require.NoError(t, err)
	v1.Status.Phase = api.VolumeReleased
	_, err = f.node2.PutVolumeStatus(ctx(t), v1)
	assert.ErrorIs(t, err, api.ErrForbidden, "pinned to another Node")
	_, err = f.node1.PutVolumeStatus(ctx(t), v1)
	assert.NoError(t, err, "control: pinned to node-1, used by no MicroVM")

	v2 := *f.v2
	v2.Status.Phase = api.VolumeAvailable
	_, err = f.node1.PutVolumeStatus(ctx(t), &v2)
	assert.ErrorIs(t, err, api.ErrForbidden, "pinned to no Node and used by none")
}

func TestNodeAuthorizer_ANodeChangesOnlyItsFinalizerOfAVolumePinnedToIt(t *testing.T) {
	f := newNodeFixture(t)
	v := f.v1
	require.Equal(t, []string{api.NodeAgentFinalizer}, v.Metadata.Finalizers, "the binding added it")

	retarget := *v
	retarget.Spec.Storage = "9Gi"
	_, err := f.node1.PutVolume(ctx(t), &retarget)
	assert.ErrorIs(t, err, api.ErrForbidden, "a spec change")
	unpin := *v
	unpin.Spec.NodeName = "node-2"
	_, err = f.node1.PutVolume(ctx(t), &unpin)
	assert.ErrorIs(t, err, api.ErrForbidden, "a node name")
	other := *v
	other.Metadata.Finalizers = []string{api.NodeAgentFinalizer, "example.com/other"}
	_, err = f.node1.PutVolume(ctx(t), &other)
	assert.ErrorIs(t, err, api.ErrForbidden, "a finalizer of another owner")
	both := *v
	both.Spec.Storage = "9Gi"
	both.Metadata.Finalizers = nil
	_, err = f.node1.PutVolume(ctx(t), &both)
	assert.ErrorIs(t, err, api.ErrForbidden, "its finalizer together with a spec change")
	_, err = f.node2.PutVolume(ctx(t), v)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node the Volume is not pinned to")
	elsewhere := *f.v2
	elsewhere.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	_, err = f.node1.PutVolume(ctx(t), &elsewhere)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Volume pinned to no Node")

	// The status and what the server sets are not the Node's to be held to.
	stale := *v
	stale.Metadata.ResourceVersion = "1"
	_, err = f.node1.PutVolume(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version is the server's to refuse, not the authorizer's")
	removed := *v
	removed.Metadata.Finalizers = nil
	removed.Status.Phase = api.VolumeReleased
	removed.Metadata.Generation = 9
	cleared, err := f.node1.PutVolume(ctx(t), &removed)
	require.NoError(t, err, "control: removing its finalizer")
	cleared.Metadata.Finalizers = []string{api.NodeAgentFinalizer}
	_, err = f.node1.PutVolume(ctx(t), cleared)
	assert.NoError(t, err, "control: adding its finalizer")
}

func TestNodeAuthorizer_ARoleThatGrantsUpdateOnVolumesLetsAnyChangeThrough(t *testing.T) {
	f := newNodeFixture(t)
	other := *f.v1
	other.Metadata.Finalizers = []string{api.NodeAgentFinalizer, "example.com/other"}
	_, err := f.node1.PutVolume(ctx(t), &other)
	require.ErrorIs(t, err, api.ErrForbidden)

	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("volume-writer", rule(api.Group, "volumes", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("volume-writer", "volume-writer", userSubject("node-1")))
	require.NoError(t, err)

	_, err = f.node1.PutVolume(ctx(t), &other)
	assert.NoError(t, err, "the same write once a role grants update on volumes")
}

func TestRBAC_TheSchedulerReadsVolumesAndTheControllersDoNot(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, _ := c.clients(t, addr, adminID)
	sched, _ := c.clients(t, addr, schedulerID)
	ctrl, _ := c.clients(t, addr, controllersID)
	_, err := admin.PutVolume(ctx(t), volume("v1"))
	require.NoError(t, err)

	_, err = ctrl.GetVolume(ctx(t), "v1")
	assert.ErrorIs(t, err, api.ErrForbidden, "the controllers' role does not read volumes")
	_, err = sched.GetVolume(ctx(t), "v1")
	assert.NoError(t, err, "control: the scheduler gets one")
	_, err = sched.ListVolumes(ctx(t))
	assert.NoError(t, err, "control: the scheduler lists them")
	w, err := sched.WatchVolumes(ctx(t), "1")
	require.NoError(t, err, "control: the scheduler watches them")
	w.Close()
	_, err = sched.PutVolume(ctx(t), volume("v2"))
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler does not create one")
}

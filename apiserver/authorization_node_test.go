package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// nodeFixture is an RBAC server with Nodes node-1 and node-2, and a Machine m1
// bound to node-1 at epoch 1 that mounts the Volume v1. The Volumes v1 and v2
// exist.
type nodeFixture struct {
	admin, node1, node2 *api.Client
	v1, v2              *api.Volume
	addr                string
	c                   *cluster
}

func newNodeFixture(t *testing.T) *nodeFixture {
	t.Helper()
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &nodeFixture{c: c, addr: addr}
	f.admin, _ = c.clients(t, addr, adminID)
	f.node1, _ = c.clients(t, addr, nodeID("node-1"))
	f.node2, _ = c.clients(t, addr, nodeID("node-2"))
	for _, n := range []string{"node-1", "node-2"} {
		_, err := f.admin.PutNode(ctx(t), node(n))
		require.NoError(t, err)
	}
	var err error
	f.v1, err = f.admin.PutVolume(ctx(t), volume("v1"))
	require.NoError(t, err)
	f.v2, err = f.admin.PutVolume(ctx(t), volume("v2"))
	require.NoError(t, err)
	m := machine("m1")
	m.Spec.Volumes = []api.MachineVolume{{Name: "v1", MountPath: "/data"}}
	m, err = f.admin.PutMachine(ctx(t), m)
	require.NoError(t, err)
	_, err = f.admin.BindMachine(ctx(t), "m1", api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: "node-1", BindingEpoch: 1})
	require.NoError(t, err)
	f.v1, err = f.admin.GetVolume(ctx(t), "v1")
	require.NoError(t, err, "the binding pinned v1 to node-1")
	return f
}

func TestNodeAuthorizer_ANodeWritesOnlyItsOwnNodeStatus(t *testing.T) {
	f := newNodeFixture(t)
	n, err := f.admin.GetNode(ctx(t), "node-1")
	require.NoError(t, err)
	n.Status.State = api.NodeStateDiscovered

	_, err = f.node2.PutNodeStatus(ctx(t), n)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.PutNodeStatus(ctx(t), n)
	assert.NoError(t, err, "control: the same write from node-1")
}

func TestNodeAuthorizer_ANodeReadsOnlyItsOwnNode(t *testing.T) {
	f := newNodeFixture(t)

	_, err := f.node2.GetNode(ctx(t), "node-1")
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.GetNode(ctx(t), "node-1")
	assert.NoError(t, err, "control: node-1 reads it")
	_, err = f.node1.ListNodes(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node's read is of its own Node, not of the list")
}

func TestNodeAuthorizer_ANodeCreatesOnlyANodeNamedAsItself(t *testing.T) {
	f := newNodeFixture(t)
	node3, _ := f.c.clients(t, f.addr, nodeID("node-3"))

	_, err := f.node1.PutNode(ctx(t), node("node-3"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = node3.PutNode(ctx(t), node("node-3"))
	assert.NoError(t, err, "control: the Node named as the certificate's CN")
}

func TestNodeAuthorizer_ANodeCannotChangeItsNodeSpec(t *testing.T) {
	f := newNodeFixture(t)
	n, err := f.admin.GetNode(ctx(t), "node-1")
	require.NoError(t, err)
	n.Spec.Admitted = true

	_, err = f.node1.PutNode(ctx(t), n)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node Agent only creates its Node")
	_, err = f.admin.PutNode(ctx(t), n)
	assert.NoError(t, err, "control: an administrator admits it")
}

func TestNodeAuthorizer_ANodeWritesOnlyItsOwnLease(t *testing.T) {
	f := newNodeFixture(t)

	_, err := f.node2.PutLease(ctx(t), lease("node-1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.PutLease(ctx(t), lease("node-1"))
	assert.NoError(t, err, "control: node-1 writes its own")
	_, err = f.node2.GetLease(ctx(t), "node-1")
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.GetLease(ctx(t), "node-1")
	assert.NoError(t, err)
}

func TestNodeAuthorizer_ANodeWritesTheStatusOfOnlyAMachineBoundToIt(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	m.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}

	_, err = f.node2.PutMachineStatus(ctx(t), m)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.PutMachineStatus(ctx(t), m)
	assert.NoError(t, err, "control: the node the Machine is bound to")
}

func TestNodeAuthorizer_ANodeUpdatesOnlyAMachineBoundToItAndCreatesNone(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	m.Metadata.Finalizers = []string{nodeAgentFinalizer}

	_, err = f.node2.PutMachine(ctx(t), m)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.PutMachine(ctx(t), machine("fresh"))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node Agent does not create a Machine")
	_, err = f.node1.PutMachine(ctx(t), m)
	assert.NoError(t, err, "control: node-1 updates the Machine bound to it")
}

func TestNodeAuthorizer_ANodeAttachesOnlyTheVolumesItsMachinesUse(t *testing.T) {
	f := newNodeFixture(t)

	_, err := f.node2.PutVolumeStatus(ctx(t), attached(f.v1, "m1", 1))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node whose Machines do not use the Volume")
	_, err = f.node1.PutVolumeStatus(ctx(t), attached(f.v2, "m1", 1))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Volume that none of its Machines uses")
	_, err = f.node1.PutVolumeStatus(ctx(t), attached(f.v1, "m1", 1))
	assert.NoError(t, err, "control: the Volume its Machine mounts")
}

const nodeAgentFinalizer = "compute.gaon.garam.sh/node-agent"

func TestNodeAuthorizer_ANodeChangesOnlyItsFinalizerOfAMachineBoundToIt(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMachine(ctx(t), "m1")
	require.NoError(t, err)

	retarget := *m
	retarget.Spec.Image = "other-image"
	_, err = f.node1.PutMachine(ctx(t), &retarget)
	assert.ErrorIs(t, err, api.ErrForbidden, "a spec change")
	other := *m
	other.Metadata.Finalizers = []string{"example.com/other"}
	_, err = f.node1.PutMachine(ctx(t), &other)
	assert.ErrorIs(t, err, api.ErrForbidden, "a finalizer of another owner")
	both := *m
	both.Spec.Image = "other-image"
	both.Metadata.Finalizers = []string{nodeAgentFinalizer}
	_, err = f.node1.PutMachine(ctx(t), &both)
	assert.ErrorIs(t, err, api.ErrForbidden, "its finalizer together with a spec change")

	added := *m
	added.Metadata.Finalizers = []string{nodeAgentFinalizer}
	added.Status.Phase = api.MachineRunning
	m, err = f.node1.PutMachine(ctx(t), &added)
	require.NoError(t, err, "control: adding its finalizer")
	removed := *m
	removed.Metadata.Finalizers = nil
	_, err = f.node1.PutMachine(ctx(t), &removed)
	assert.NoError(t, err, "control: removing its finalizer")
}

func TestNodeAuthorizer_ARoleThatGrantsUpdateOnMachinesAllowsASpecChange(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	m.Spec.IdleTimeoutSeconds = 30
	_, err = f.node1.PutMachine(ctx(t), m)
	require.ErrorIs(t, err, api.ErrForbidden)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("machine-writer", rule(api.Group, "machines", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("machine-writer", "machine-writer", userSubject("node-1")))
	require.NoError(t, err)

	_, err = f.node1.PutMachine(ctx(t), m)
	assert.NoError(t, err)
}

func TestNodeAuthorizer_ANodeReadsMachinesAndAStrangerDoesNot(t *testing.T) {
	f := newNodeFixture(t)
	ghost, _ := f.c.clients(t, f.addr, nodeID("ghost"))

	_, err := ghost.ListMachines(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "no Node is named ghost")
	_, err = ghost.GetMachine(ctx(t), "m1")
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node2.ListMachines(ctx(t))
	assert.NoError(t, err, "control: every Node reads Machines, bound to it or not")
	_, err = f.node2.GetMachine(ctx(t), "m1")
	assert.NoError(t, err)
}

func TestNodeAuthorizer_ANodeCreatesACertificateSigningRequestAndReadsItsOwn(t *testing.T) {
	f := newNodeFixture(t)
	ghost, _ := f.c.clients(t, f.addr, nodeID("ghost"))

	_, err := ghost.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	assert.ErrorIs(t, err, api.ErrForbidden, "no Node is named ghost")
	_, err = f.node1.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	require.NoError(t, err, "control: a Node creates one")

	_, err = f.node2.GetCertificateSigningRequest(ctx(t), "node-1")
	assert.ErrorIs(t, err, api.ErrForbidden, "another Node's request")
	_, err = f.node1.GetCertificateSigningRequest(ctx(t), "node-1")
	assert.NoError(t, err, "control: the request node-1 created")
}

func TestNodeAuthorizer_NeverRefusesWhatARoleAllows(t *testing.T) {
	f := newNodeFixture(t)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	n, err := f.admin.GetNode(ctx(t), "node-1")
	require.NoError(t, err)
	n.Status.State = api.NodeStateDiscovered
	_, err = f.node2.PutNodeStatus(ctx(t), n)
	require.ErrorIs(t, err, api.ErrForbidden)

	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("node-status", rule(api.Group, "nodes/status", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("node-status", "node-status", userSubject("node-2")))
	require.NoError(t, err)

	_, err = f.node2.PutNodeStatus(ctx(t), n)
	assert.NoError(t, err, "a Node that is not its own Node's owner is allowed by a role bound to it")
}

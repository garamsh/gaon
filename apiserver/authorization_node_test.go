package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// nodeFixture is an RBAC server with Nodes node-1 and node-2, and a MicroVM m1
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
	m := microvm("m1")
	m.Spec.Volumes = []api.MicroVMVolume{{Name: "v1", MountPath: "/data"}}
	m, err = f.admin.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	_, err = f.admin.BindMicroVM(ctx(t), "m1", api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: "node-1", BindingEpoch: 1})
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

func TestNodeAuthorizer_ANodeWritesTheStatusOfOnlyAMicroVMBoundToIt(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)
	m.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, BindingEpoch: 1}

	_, err = f.node2.PutMicroVMStatus(ctx(t), m)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.PutMicroVMStatus(ctx(t), m)
	assert.NoError(t, err, "control: the node the MicroVM is bound to")
}

func TestNodeAuthorizer_ANodeUpdatesOnlyAMicroVMBoundToItAndCreatesNone(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)
	m.Metadata.Finalizers = []string{nodeAgentFinalizer}

	_, err = f.node2.PutMicroVM(ctx(t), m)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.PutMicroVM(ctx(t), microvm("fresh"))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node Agent does not create a MicroVM")
	_, err = f.node1.PutMicroVM(ctx(t), m)
	assert.NoError(t, err, "control: node-1 updates the MicroVM bound to it")
}

func TestNodeAuthorizer_ANodeAttachesOnlyTheVolumesItsMicroVMsUse(t *testing.T) {
	f := newNodeFixture(t)

	_, err := f.node2.PutVolumeStatus(ctx(t), attached(f.v1, "m1", 1))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node whose MicroVMs do not use the Volume")
	_, err = f.node1.PutVolumeStatus(ctx(t), attached(f.v2, "m1", 1))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Volume that none of its MicroVMs uses")
	_, err = f.node1.PutVolumeStatus(ctx(t), attached(f.v1, "m1", 1))
	assert.NoError(t, err, "control: the Volume its MicroVM mounts")
}

const nodeAgentFinalizer = "compute.gaon.garam.sh/node-agent"

func TestNodeAuthorizer_ANodeChangesOnlyItsFinalizerOfAMicroVMBoundToIt(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)

	retarget := *m
	retarget.Spec.Image = "other-image"
	_, err = f.node1.PutMicroVM(ctx(t), &retarget)
	assert.ErrorIs(t, err, api.ErrForbidden, "a spec change")
	other := *m
	other.Metadata.Finalizers = []string{"example.com/other"}
	_, err = f.node1.PutMicroVM(ctx(t), &other)
	assert.ErrorIs(t, err, api.ErrForbidden, "a finalizer of another owner")
	both := *m
	both.Spec.Image = "other-image"
	both.Metadata.Finalizers = []string{nodeAgentFinalizer}
	_, err = f.node1.PutMicroVM(ctx(t), &both)
	assert.ErrorIs(t, err, api.ErrForbidden, "its finalizer together with a spec change")

	added := *m
	added.Metadata.Finalizers = []string{nodeAgentFinalizer}
	added.Status.Phase = api.MicroVMRunning
	m, err = f.node1.PutMicroVM(ctx(t), &added)
	require.NoError(t, err, "control: adding its finalizer")
	removed := *m
	removed.Metadata.Finalizers = nil
	_, err = f.node1.PutMicroVM(ctx(t), &removed)
	assert.NoError(t, err, "control: removing its finalizer")
}

func TestNodeAuthorizer_ARoleThatGrantsUpdateOnMicroVMsAllowsASpecChange(t *testing.T) {
	f := newNodeFixture(t)
	m, err := f.admin.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)
	m.Spec.IdleTimeoutSeconds = 30
	_, err = f.node1.PutMicroVM(ctx(t), m)
	require.ErrorIs(t, err, api.ErrForbidden)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("microvm-writer", rule(api.Group, "microvms", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("microvm-writer", "microvm-writer", userSubject("node-1")))
	require.NoError(t, err)

	_, err = f.node1.PutMicroVM(ctx(t), m)
	assert.NoError(t, err)
}

func TestNodeAuthorizer_ANodeReadsMicroVMsAndAStrangerDoesNot(t *testing.T) {
	f := newNodeFixture(t)
	ghost, _ := f.c.clients(t, f.addr, nodeID("ghost"))

	_, err := ghost.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "no Node is named ghost")
	_, err = ghost.GetMicroVM(ctx(t), "m1")
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node2.ListMicroVMs(ctx(t))
	assert.NoError(t, err, "control: every Node reads MicroVMs, bound to it or not")
	_, err = f.node2.GetMicroVM(ctx(t), "m1")
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

func TestNodeAuthorizer_ASelectorRoleLetsANodeUpdateOnlyTheMicroVMsInsideIt(t *testing.T) {
	f := newNodeFixture(t)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	selected := rule(api.Group, "microvms", api.VerbUpdate)
	selected.LabelSelector = "app=web"
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("web-writer", selected))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("web-writer", "web-writer", userSubject("node-1")))
	require.NoError(t, err)
	bound := func(name, app string) *api.MicroVM {
		m, err := f.admin.PutMicroVM(ctx(t), labeled(microvm(name), "app", app))
		require.NoError(t, err)
		_, err = f.admin.BindMicroVM(ctx(t), name, api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: "node-1", BindingEpoch: 1})
		require.NoError(t, err)
		m, err = f.admin.GetMicroVM(ctx(t), name)
		require.NoError(t, err)
		return m
	}
	web, db := bound("w1", "web"), bound("d1", "db")

	outside := *db
	outside.Spec.IdleTimeoutSeconds = 30
	_, err = f.node1.PutMicroVM(ctx(t), &outside)
	assert.ErrorIs(t, err, api.ErrForbidden, "a full update of a bound MicroVM outside the selector")
	finalizer := *db
	finalizer.Metadata.Finalizers = []string{nodeAgentFinalizer}
	_, err = f.node1.PutMicroVM(ctx(t), &finalizer)
	assert.NoError(t, err, "control: its finalizer on the same MicroVM")

	inside := *web
	inside.Spec.IdleTimeoutSeconds = 30
	_, err = f.node1.PutMicroVM(ctx(t), &inside)
	assert.NoError(t, err, "control: a full update of a bound MicroVM inside the selector")
}

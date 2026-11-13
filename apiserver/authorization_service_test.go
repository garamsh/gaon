package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// serviceFixture is an RBAC server with the Node node-1 and a Service, an
// Endpoints and a NetworkPolicy named web.
type serviceFixture struct {
	admin, node1, stranger, controllers, scheduler *api.Client
	// unregistered holds a Node certificate for a Node that does not exist.
	unregistered *api.Client
	svc          *api.Service
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	c := newCluster(t)
	c.cfg.ServiceClusterIPRange = "10.96.0.0/24"
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &serviceFixture{}
	f.admin, _ = c.clients(t, addr, adminID)
	f.node1, _ = c.clients(t, addr, nodeID("node-1"))
	f.unregistered, _ = c.clients(t, addr, nodeID("node-9"))
	f.stranger, _ = c.clients(t, addr, strangerID)
	f.controllers, _ = c.clients(t, addr, controllersID)
	f.scheduler, _ = c.clients(t, addr, schedulerID)
	_, err := f.admin.PutNode(ctx(t), node("node-1"))
	require.NoError(t, err)
	f.svc, err = f.admin.PutService(ctx(t), newSvc("web"))
	require.NoError(t, err)
	_, err = f.admin.PutEndpoints(ctx(t), newEndpoints("web"))
	require.NoError(t, err)
	_, err = f.admin.PutNetworkPolicy(ctx(t), newPolicy("web"))
	require.NoError(t, err)
	return f
}

func TestNodeAuthorizer_ANodeReadsServicesEndpointsAndNetworkPoliciesAndWritesNone(t *testing.T) {
	f := newServiceFixture(t)

	_, err := f.node1.GetService(ctx(t), "web")
	assert.NoError(t, err)
	_, err = f.node1.ListServices(ctx(t))
	assert.NoError(t, err)
	w, err := f.node1.WatchServices(ctx(t), f.svc.Metadata.ResourceVersion)
	if assert.NoError(t, err) {
		_ = w.Close()
	}
	_, err = f.node1.GetEndpoints(ctx(t), "web")
	assert.NoError(t, err)
	_, err = f.node1.ListEndpoints(ctx(t))
	assert.NoError(t, err)
	_, err = f.node1.GetNetworkPolicy(ctx(t), "web")
	assert.NoError(t, err)
	_, err = f.node1.ListNetworkPolicies(ctx(t))
	assert.NoError(t, err)

	_, err = f.node1.PutService(ctx(t), newSvc("mine"))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node creates no Service")
	_, err = f.node1.PutEndpoints(ctx(t), newEndpoints("mine"))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node creates no Endpoints")
	_, err = f.node1.PutNetworkPolicy(ctx(t), newPolicy("mine"))
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node creates no NetworkPolicy")
	changed := *f.svc
	changed.Spec.Ports = []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 81}}
	_, err = f.node1.PutService(ctx(t), &changed)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node changes no Service")
	_, err = f.node1.DeleteService(ctx(t), "web", f.svc.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node deletes no Service")
	_, err = f.admin.PutService(ctx(t), &changed)
	assert.NoError(t, err, "control: the same write by an administrator")
}

func TestNodeAuthorizer_AReadOfTheServiceKindsNeedsANodeThatExists(t *testing.T) {
	f := newServiceFixture(t)

	_, err := f.unregistered.GetService(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node certificate of a Node that does not exist")
	_, err = f.unregistered.ListEndpoints(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.stranger.ListNetworkPolicies(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "a caller that is no Node")
	_, err = f.node1.ListServices(ctx(t))
	assert.NoError(t, err, "control: a Node that exists")
}

func TestBootstrap_TheControllersWriteEndpointsAndNoOtherRoleDoes(t *testing.T) {
	f := newServiceFixture(t)

	made, err := f.controllers.PutEndpoints(ctx(t), newEndpoints("made"))
	require.NoError(t, err, "the controllers create an Endpoints")
	made.Subsets = nil
	_, err = f.controllers.PutEndpoints(ctx(t), made)
	assert.NoError(t, err, "the controllers update one")
	_, err = f.controllers.GetEndpoints(ctx(t), "web")
	assert.NoError(t, err, "the controllers read the kinds")
	_, err = f.controllers.PutService(ctx(t), newSvc("nope"))
	assert.ErrorIs(t, err, api.ErrForbidden, "the controllers create no Service")
	_, err = f.controllers.PutNetworkPolicy(ctx(t), newPolicy("nope"))
	assert.ErrorIs(t, err, api.ErrForbidden, "the controllers create no NetworkPolicy")
	current, err := f.controllers.GetEndpoints(ctx(t), "made")
	require.NoError(t, err)
	_, err = f.controllers.DeleteEndpoints(ctx(t), "made", current.Metadata.ResourceVersion)
	assert.NoError(t, err, "the controllers delete what they read, as ADR 0021 gives them")

	_, err = f.scheduler.PutEndpoints(ctx(t), newEndpoints("sched"))
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler writes no Endpoints")
	_, err = f.scheduler.GetService(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler reads no Service")
	_, err = f.stranger.PutEndpoints(ctx(t), newEndpoints("stranger"))
	assert.ErrorIs(t, err, api.ErrForbidden, "control: a caller without a role")
}

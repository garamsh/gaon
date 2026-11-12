package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// editorFixture is an RBAC server where the user editor holds create, update,
// get and list on clusterroles and clusterrolebindings, and nothing else.
type editorFixture struct {
	adminRBAC  *api.Client
	editorRBAC *api.Client
	addr       string
	c          *cluster
}

func newEditorFixture(t *testing.T, verbs ...api.Verb) *editorFixture {
	t.Helper()
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &editorFixture{c: c, addr: addr}
	_, f.adminRBAC = c.clients(t, addr, adminID)
	_, f.editorRBAC = c.clients(t, addr, who{"editor", nil})
	if len(verbs) == 0 {
		verbs = []api.Verb{api.VerbCreate, api.VerbUpdate, api.VerbGet, api.VerbList}
	}
	_, err := f.adminRBAC.PutClusterRole(ctx(t), clusterRole("role-editor",
		rule(api.RBACGroup, "clusterroles", verbs...),
		rule(api.RBACGroup, "clusterrolebindings", verbs...)))
	require.NoError(t, err)
	_, err = f.adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("role-editor", "role-editor", userSubject("editor")))
	require.NoError(t, err)
	return f
}

func TestEscalation_ARoleGrantingWhatTheCallerLacksIsForbidden(t *testing.T) {
	f := newEditorFixture(t)
	reader := clusterRole("machine-reader", rule(api.Group, "machines", api.VerbGet))

	_, err := f.editorRBAC.PutClusterRole(ctx(t), reader)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.adminRBAC.GetClusterRole(ctx(t), "machine-reader")
	assert.ErrorIs(t, err, api.ErrNotFound, "a refused role is not stored")
	_, err = f.adminRBAC.PutClusterRole(ctx(t), reader)
	assert.NoError(t, err, "control: the same request from a caller holding get on machines")
}

func TestEscalation_ARoleWithinWhatTheCallerHoldsIsCreated(t *testing.T) {
	f := newEditorFixture(t)

	_, err := f.editorRBAC.PutClusterRole(ctx(t), clusterRole("role-lister", rule(api.RBACGroup, "clusterroles", api.VerbList)))

	assert.NoError(t, err)
}

func TestEscalation_AnUpdateThatAddsAPermissionTheCallerLacksIsForbidden(t *testing.T) {
	f := newEditorFixture(t)
	held, err := f.editorRBAC.PutClusterRole(ctx(t), clusterRole("mine", rule(api.RBACGroup, "clusterroles", api.VerbGet)))
	require.NoError(t, err)

	wider := *held
	wider.Rules = []api.PolicyRule{rule(api.RBACGroup, "clusterroles", api.VerbGet), rule(api.Group, "nodes", api.VerbList)}
	_, err = f.editorRBAC.PutClusterRole(ctx(t), &wider)
	assert.ErrorIs(t, err, api.ErrForbidden)
	narrower := *held
	narrower.Rules = []api.PolicyRule{rule(api.RBACGroup, "clusterroles", api.VerbList)}
	_, err = f.editorRBAC.PutClusterRole(ctx(t), &narrower)
	assert.NoError(t, err, "control: an update within what the caller holds")
}

func TestEscalation_AWildcardIsHeldOnlyByAWildcard(t *testing.T) {
	f := newEditorFixture(t)
	star := clusterRole("star-verbs", rule(api.RBACGroup, "clusterroles", api.VerbAll))

	_, err := f.editorRBAC.PutClusterRole(ctx(t), star)
	assert.ErrorIs(t, err, api.ErrForbidden, "four verbs are not *")
	_, err = f.adminRBAC.PutClusterRole(ctx(t), star)
	assert.NoError(t, err, "control: a caller holding * grants *")
	groups := clusterRole("star-groups", api.PolicyRule{APIGroups: []string{"*"}, Resources: []string{"clusterroles"}, Verbs: []api.Verb{api.VerbGet}})
	_, err = f.editorRBAC.PutClusterRole(ctx(t), groups)
	assert.ErrorIs(t, err, api.ErrForbidden, "one group is not *")
	resources := clusterRole("star-resources", rule(api.RBACGroup, "*", api.VerbGet))
	_, err = f.editorRBAC.PutClusterRole(ctx(t), resources)
	assert.ErrorIs(t, err, api.ErrForbidden, "two resources are not *")
}

func TestEscalation_ARoleIsCheckedRuleByRule(t *testing.T) {
	f := newEditorFixture(t)
	two := clusterRole("two", rule(api.RBACGroup, "clusterroles", api.VerbGet), rule(api.Group, "machines", api.VerbGet))

	_, err := f.editorRBAC.PutClusterRole(ctx(t), two)
	assert.ErrorIs(t, err, api.ErrForbidden, "the second rule is not held")
	two.Rules = two.Rules[:1]
	_, err = f.editorRBAC.PutClusterRole(ctx(t), two)
	assert.NoError(t, err, "control: the held rule alone")
}

func TestEscalation_ABindingToARoleTheCallerDoesNotHoldIsForbidden(t *testing.T) {
	f := newEditorFixture(t)

	_, err := f.editorRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("self-admin", "gaon:admin", userSubject("editor")))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.adminRBAC.GetClusterRoleBinding(ctx(t), "self-admin")
	assert.ErrorIs(t, err, api.ErrNotFound, "a refused binding is not stored")
	_, err = f.adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("self-admin", "gaon:admin", userSubject("editor")))
	assert.NoError(t, err, "control: the same request from a caller holding every permission of the role")
}

func TestEscalation_ABindingToARoleTheCallerHoldsIsCreated(t *testing.T) {
	f := newEditorFixture(t)

	_, err := f.editorRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("share", "role-editor", userSubject("other")))

	assert.NoError(t, err)
}

func TestEscalation_ABindingToAMissingRoleIsInvalid(t *testing.T) {
	f := newEditorFixture(t)

	_, err := f.editorRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("ghost", "no-such-role", userSubject("other")))
	assert.ErrorIs(t, err, api.ErrInvalid)
	_, err = f.editorRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("ghost", "role-editor", userSubject("other")))
	assert.NoError(t, err, "control: a role that exists")
}

func TestEscalation_ABindingUpdateIsCheckedLikeACreate(t *testing.T) {
	f := newEditorFixture(t)
	held, err := f.editorRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("share", "role-editor", userSubject("other")))
	require.NoError(t, err)
	_, err = f.adminRBAC.PutClusterRole(ctx(t), clusterRole("node-reader", rule(api.Group, "nodes", api.VerbGet)))
	require.NoError(t, err)
	_, err = f.adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("nodes", "node-reader", userSubject("someone")))
	require.NoError(t, err)

	moved := *held
	moved.Subjects = []api.Subject{userSubject("third")}
	_, err = f.editorRBAC.PutClusterRoleBinding(ctx(t), &moved)
	assert.NoError(t, err, "control: changing the subjects of a binding to a role the caller holds")
	other, err := f.adminRBAC.GetClusterRoleBinding(ctx(t), "nodes")
	require.NoError(t, err)
	other.Subjects = []api.Subject{userSubject("editor")}
	_, err = f.editorRBAC.PutClusterRoleBinding(ctx(t), other)
	assert.ErrorIs(t, err, api.ErrForbidden, "changing the subjects of a binding to a role the caller does not hold")
}

func TestEscalation_DeletingNeedsOnlyDelete(t *testing.T) {
	f := newEditorFixture(t, api.VerbCreate, api.VerbUpdate, api.VerbGet, api.VerbList, api.VerbDelete)
	role, err := f.adminRBAC.PutClusterRole(ctx(t), clusterRole("machine-reader", rule(api.Group, "machines", api.VerbGet)))
	require.NoError(t, err)
	viewer := clusterRole("viewer", rule(api.RBACGroup, "clusterroles", api.VerbGet))
	_, err = f.adminRBAC.PutClusterRole(ctx(t), viewer)
	require.NoError(t, err)
	_, err = f.adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("viewer", "viewer", userSubject("viewer")))
	require.NoError(t, err)
	_, viewerRBAC := f.c.clients(t, f.addr, who{"viewer", nil})

	_, err = viewerRBAC.DeleteClusterRole(ctx(t), "machine-reader", role.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "a caller with get alone")
	_, err = f.editorRBAC.DeleteClusterRole(ctx(t), "machine-reader", role.Metadata.ResourceVersion)
	assert.NoError(t, err, "a caller with delete deletes a role that grants what it does not hold")
}

func TestEscalation_TheNodeAuthorizersPermissionsAreNotCounted(t *testing.T) {
	f := newNodeFixture(t)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("role-editor", rule(api.RBACGroup, "clusterroles", api.VerbCreate, api.VerbGet)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("role-editor", "role-editor", userSubject("node-1")))
	require.NoError(t, err)
	_, nodeRBAC := f.c.clients(t, f.addr, nodeID("node-1"))
	_, err = f.node1.ListMachines(ctx(t))
	require.NoError(t, err, "the Node authorizer lets node-1 list Machines")

	_, err = nodeRBAC.PutClusterRole(ctx(t), clusterRole("machine-lister", rule(api.Group, "machines", api.VerbList)))
	assert.ErrorIs(t, err, api.ErrForbidden, "a role granting list on machines")
	_, err = nodeRBAC.PutClusterRole(ctx(t), clusterRole("role-getter", rule(api.RBACGroup, "clusterroles", api.VerbGet)))
	assert.NoError(t, err, "control: a role within what a bound role grants it")
}

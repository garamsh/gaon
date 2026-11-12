package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// rbacClient is the client of the RBAC group on the plain HTTP server of e,
// which authorizes nobody.
func (e *env) rbacClient() *api.Client {
	return api.NewClient(e.base+rbacAPIRoot, nil)
}

func TestPutClusterRole_CreatesThenUpdatesOnTheCurrentVersion(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()

	created, err := rbac.PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)

	update := *created
	update.Rules = []api.PolicyRule{rule(api.Group, "microvms", api.VerbList)}
	updated, err := rbac.PutClusterRole(ctx(t), &update)
	require.NoError(t, err)
	assert.NotEqual(t, created.Metadata.ResourceVersion, updated.Metadata.ResourceVersion)
	assert.Equal(t, update.Rules, updated.Rules)

	_, err = rbac.PutClusterRole(ctx(t), created)
	assert.ErrorIs(t, err, api.ErrConflict, "the first version is stale")
}

func TestPutClusterRole_CreateOfAnExistingNameIsAlreadyExists(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()
	_, err := rbac.PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)

	_, err = rbac.PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "nodes", api.VerbGet)))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)
	_, err = rbac.PutClusterRole(ctx(t), clusterRole("r2", rule(api.Group, "nodes", api.VerbGet)))
	assert.NoError(t, err, "control: a new name")
}

func TestPutClusterRole_RefusesARoleThatIsNotValid(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()

	_, err := rbac.PutClusterRole(ctx(t), clusterRole("bogus", rule(api.Group, "microvms", "bogus")))
	assert.ErrorIs(t, err, api.ErrInvalid, "an unknown verb")
	_, err = rbac.PutClusterRole(ctx(t), clusterRole("fine", rule(api.Group, "microvms", api.VerbGet)))
	assert.NoError(t, err, "control: a rule")
}

func TestClusterRole_GetListAndDelete(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()
	r1, err := rbac.PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)
	_, err = rbac.PutClusterRole(ctx(t), clusterRole("r2", rule(api.Group, "nodes", api.VerbGet)))
	require.NoError(t, err)

	got, err := rbac.GetClusterRole(ctx(t), "r1")
	require.NoError(t, err)
	assert.Equal(t, r1, got)
	list, err := rbac.ListClusterRoles(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, api.KindClusterRoleList, list.Kind)
	assert.Equal(t, api.RBACGroupVersion, list.APIVersion)

	_, err = rbac.DeleteClusterRole(ctx(t), "r1", "1")
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = rbac.DeleteClusterRole(ctx(t), "r1", r1.Metadata.ResourceVersion)
	assert.NoError(t, err)
	_, err = rbac.GetClusterRole(ctx(t), "r1")
	assert.ErrorIs(t, err, api.ErrNotFound)
	_, err = rbac.DeleteClusterRole(ctx(t), "r1", r1.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClusterRole_AWatchSeesAnAddedRole(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()
	list, err := rbac.ListClusterRoles(ctx(t))
	require.NoError(t, err)
	w, err := rbac.WatchClusterRoles(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	_, err = rbac.PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)

	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "r1", ev.Object.Metadata.Name)
}

func TestPutClusterRoleBinding_RoleRefCannotChangeAndSubjectsCan(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()
	created, err := rbac.PutClusterRoleBinding(ctx(t), clusterRoleBinding("b1", "r1", userSubject("alice")))
	require.NoError(t, err)

	retarget := *created
	retarget.RoleRef.Name = "r2"
	_, err = rbac.PutClusterRoleBinding(ctx(t), &retarget)
	assert.ErrorIs(t, err, api.ErrInvalid)
	resubject := *created
	resubject.Subjects = []api.Subject{userSubject("bob"), groupSubject("team")}
	updated, err := rbac.PutClusterRoleBinding(ctx(t), &resubject)
	require.NoError(t, err, "control: the subjects change")
	assert.Equal(t, resubject.Subjects, updated.Subjects)
	assert.Equal(t, "r1", updated.RoleRef.Name)
}

func TestClusterRoleBinding_GetListAndDelete(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()
	b1, err := rbac.PutClusterRoleBinding(ctx(t), clusterRoleBinding("b1", "r1", userSubject("alice")))
	require.NoError(t, err)
	_, err = rbac.PutClusterRoleBinding(ctx(t), clusterRoleBinding("b2", "r1", userSubject("bob")))
	require.NoError(t, err)

	got, err := rbac.GetClusterRoleBinding(ctx(t), "b1")
	require.NoError(t, err)
	assert.Equal(t, b1, got)
	list, err := rbac.ListClusterRoleBindings(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, api.KindClusterRoleBindingList, list.Kind)

	_, err = rbac.DeleteClusterRoleBinding(ctx(t), "b1", "1")
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = rbac.DeleteClusterRoleBinding(ctx(t), "b1", b1.Metadata.ResourceVersion)
	assert.NoError(t, err)
	_, err = rbac.GetClusterRoleBinding(ctx(t), "b1")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClusterRoleBinding_AWatchSeesADeletedBinding(t *testing.T) {
	e := newEnv(t)
	rbac := e.rbacClient()
	b1, err := rbac.PutClusterRoleBinding(ctx(t), clusterRoleBinding("b1", "r1", userSubject("alice")))
	require.NoError(t, err)
	w, err := rbac.WatchClusterRoleBindings(ctx(t), b1.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	_, err = rbac.DeleteClusterRoleBinding(ctx(t), "b1", b1.Metadata.ResourceVersion)
	require.NoError(t, err)

	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventDeleted, ev.Type)
}

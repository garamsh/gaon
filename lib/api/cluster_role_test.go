package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newClusterRole(name string) *api.ClusterRole {
	return &api.ClusterRole{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRole},
		Metadata: api.ObjectMeta{Name: name},
		Rules: []api.PolicyRule{{
			APIGroups: []string{api.Group},
			Resources: []string{"machines", "machines/status"},
			Verbs:     []string{api.VerbGet, api.VerbList},
		}},
	}
}

func TestClusterRoleValidateRefusesAnUnknownVerb(t *testing.T) {
	good := newClusterRole("reader")
	good.Rules[0].Verbs = []string{"get", "list", "watch", "create", "update", "delete", "*"}
	require.NoError(t, good.Validate(), "control: every known verb, and the wildcard, is accepted")

	for _, verb := range []string{"", "patch", "Get", "bind", "escalate", "get*"} {
		role := newClusterRole("reader")
		role.Rules[0].Verbs = []string{"get", verb}
		assert.ErrorIs(t, role.Validate(), api.ErrInvalid, "verb %q", verb)
	}
}

func TestClusterRoleValidateRefusesARuleThatMatchesNothing(t *testing.T) {
	for name, edit := range map[string]func(*api.PolicyRule){
		"no verbs":          func(r *api.PolicyRule) { r.Verbs = nil },
		"no resources":      func(r *api.PolicyRule) { r.Resources = nil },
		"no apiGroups":      func(r *api.PolicyRule) { r.APIGroups = nil },
		"an empty resource": func(r *api.PolicyRule) { r.Resources = []string{"machines", ""} },
		"an empty apiGroup": func(r *api.PolicyRule) { r.APIGroups = []string{""} },
	} {
		role := newClusterRole("reader")
		edit(&role.Rules[0])
		assert.ErrorIs(t, role.Validate(), api.ErrInvalid, name)
	}

	wildcards := newClusterRole("admin")
	wildcards.Rules = []api.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}
	assert.NoError(t, wildcards.Validate(), "control: the wildcard is a value in each field")
	empty := newClusterRole("empty")
	empty.Rules = nil
	assert.NoError(t, empty.Validate(), "control: a role with no rules grants nothing and is valid")
}

func TestClusterRoleValidateChecksIdentity(t *testing.T) {
	role := newClusterRole("reader")
	role.APIVersion = api.GroupVersion
	assert.ErrorIs(t, role.Validate(), api.ErrInvalid, "a ClusterRole is in the rbac group, not the compute group")
	role = newClusterRole("reader")
	role.Kind = api.KindClusterRoleBinding
	assert.ErrorIs(t, role.Validate(), api.ErrInvalid)
	assert.ErrorIs(t, newClusterRole("").Validate(), api.ErrInvalid)
}

func TestClientClusterRolePutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetClusterRole(ctx, "reader")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutClusterRole(ctx, newClusterRole("reader"))
	require.NoError(t, err)
	_, err = c.PutClusterRole(ctx, newClusterRole("reader"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	update := *created
	update.Rules = append([]api.PolicyRule{}, created.Rules...)
	update.Rules[0].Verbs = []string{api.VerbWatch}
	updated, err := c.PutClusterRole(ctx, &update)
	require.NoError(t, err)
	_, err = c.PutClusterRole(ctx, &update)
	assert.ErrorIs(t, err, api.ErrConflict, "the first update made the version stale")

	got, err := c.GetClusterRole(ctx, "reader")
	require.NoError(t, err)
	assert.Equal(t, []string{api.VerbWatch}, got.Rules[0].Verbs)

	list, err := c.ListClusterRoles(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindClusterRoleList, list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, updated.Metadata.ResourceVersion, list.Metadata.ResourceVersion)

	_, err = c.DeleteClusterRole(ctx, "reader", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = c.DeleteClusterRole(ctx, "reader", "")
	assert.Error(t, err, "a delete names the version it deletes")
	_, err = c.DeleteClusterRole(ctx, "reader", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetClusterRole(ctx, "reader")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClientWatchClusterRolesDeliversClusterRoleEventsOnly(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	_, err := c.PutClusterRoleBinding(ctx, newClusterRoleBinding("reader-binding"))
	require.NoError(t, err)
	created, err := c.PutClusterRole(ctx, newClusterRole("reader"))
	require.NoError(t, err)

	w, err := c.WatchClusterRoles(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "reader", ev.Object.Metadata.Name, "the binding's event is not delivered to a ClusterRole watch")

	_, err = c.DeleteClusterRole(ctx, "reader", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type)
	assert.Equal(t, "reader", ev.Object.Metadata.Name)

	s.compact()
	stale, err := c.WatchClusterRoles(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)

	_, err = c.WatchClusterRoles(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}

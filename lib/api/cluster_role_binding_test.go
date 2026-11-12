package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newClusterRoleBinding(name string) *api.ClusterRoleBinding {
	return &api.ClusterRoleBinding{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBinding},
		Metadata: api.ObjectMeta{Name: name},
		RoleRef:  api.RoleRef{Kind: api.KindClusterRole, Name: "reader"},
		Subjects: []api.Subject{{Kind: api.SubjectUser, Name: "alice"}, {Kind: api.SubjectGroup, Name: "gaon:admins"}},
	}
}

func TestClusterRoleBindingValidateRefusesAnEmptyRoleRef(t *testing.T) {
	require.NoError(t, newClusterRoleBinding("b").Validate(), "control: a roleRef naming a ClusterRole is accepted")

	for name, ref := range map[string]api.RoleRef{
		"empty":          {},
		"no name":        {Kind: api.KindClusterRole},
		"no kind":        {Name: "reader"},
		"another kind":   {Kind: "Role", Name: "reader"},
		"kind in a case": {Kind: "clusterrole", Name: "reader"},
	} {
		b := newClusterRoleBinding("b")
		b.RoleRef = ref
		assert.ErrorIs(t, b.Validate(), api.ErrInvalid, name)
	}
}

func TestClusterRoleBindingValidateRefusesAnUnknownSubjectKind(t *testing.T) {
	for _, kind := range []api.SubjectKind{api.SubjectUser, api.SubjectGroup} {
		b := newClusterRoleBinding("b")
		b.Subjects = []api.Subject{{Kind: kind, Name: "x"}}
		assert.NoError(t, b.Validate(), "control: "+kind)
	}
	for _, kind := range []api.SubjectKind{"", "ServiceAccount", "user", "Node"} {
		b := newClusterRoleBinding("b")
		b.Subjects = []api.Subject{{Kind: api.SubjectUser, Name: "alice"}, {Kind: kind, Name: "x"}}
		assert.ErrorIs(t, b.Validate(), api.ErrInvalid, "kind %q", kind)
	}
}

func TestClusterRoleBindingValidateRefusesASubjectWithNoName(t *testing.T) {
	for _, kind := range []api.SubjectKind{api.SubjectUser, api.SubjectGroup} {
		named := newClusterRoleBinding("b")
		named.Subjects = []api.Subject{{Kind: kind, Name: "x"}}
		assert.NoError(t, named.Validate(), "control: a named "+kind)

		nameless := newClusterRoleBinding("b")
		nameless.Subjects = []api.Subject{{Kind: kind}}
		assert.ErrorIs(t, nameless.Validate(), api.ErrInvalid, "a nameless "+kind)
	}
}

func TestClusterRoleBindingValidateChecksIdentity(t *testing.T) {
	require.NoError(t, newClusterRoleBinding("b").Validate(), "control: the rbac group, the right kind and a name")

	b := newClusterRoleBinding("b")
	b.APIVersion = api.GroupVersion
	assert.ErrorIs(t, b.Validate(), api.ErrInvalid, "a binding is in the rbac group, not the compute group")
	b = newClusterRoleBinding("b")
	b.Kind = api.KindClusterRole
	assert.ErrorIs(t, b.Validate(), api.ErrInvalid)
	assert.ErrorIs(t, newClusterRoleBinding("").Validate(), api.ErrInvalid)
}

func TestClientClusterRoleBindingPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	created, err := c.PutClusterRoleBinding(ctx, newClusterRoleBinding("b"))
	require.NoError(t, err)
	_, err = c.PutClusterRoleBinding(ctx, newClusterRoleBinding("b"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	invalid := newClusterRoleBinding("bad")
	invalid.RoleRef = api.RoleRef{}
	_, err = c.PutClusterRoleBinding(ctx, invalid)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.GetClusterRoleBinding(ctx, "b")
	require.NoError(t, err)
	assert.Equal(t, "reader", got.RoleRef.Name)
	assert.Len(t, got.Subjects, 2)

	list, err := c.ListClusterRoleBindings(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindClusterRoleBindingList, list.Kind)
	assert.Len(t, list.Items, 1)

	_, err = c.DeleteClusterRoleBinding(ctx, "b", "")
	assert.Error(t, err, "a delete names the version it deletes")
	_, err = c.DeleteClusterRoleBinding(ctx, "b", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetClusterRoleBinding(ctx, "b")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClientWatchClusterRoleBindingsDeliversBindingEventsOnly(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	_, err := c.PutClusterRole(ctx, newClusterRole("reader"))
	require.NoError(t, err)
	_, err = c.PutClusterRoleBinding(ctx, newClusterRoleBinding("b"))
	require.NoError(t, err)

	w, err := c.WatchClusterRoleBindings(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "b", ev.Object.Metadata.Name, "the role's event is not delivered to a binding watch")
	assert.Equal(t, api.KindClusterRoleBinding, ev.Object.Kind)
}

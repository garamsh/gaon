package apiserver_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

func TestBootstrap_CreatesTheRolesAndBindingsAtStart(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, admin := c.clients(t, addr, adminID)

	for role, group := range map[string]string{
		"gaon:scheduler":   "gaon:schedulers",
		"gaon:controllers": "gaon:controllers",
		"gaon:admin":       "gaon:admins",
	} {
		r, err := admin.GetClusterRole(ctx(t), role)
		require.NoError(t, err, role)
		assert.NotEmpty(t, r.Rules, role)
		b, err := admin.GetClusterRoleBinding(ctx(t), role)
		require.NoError(t, err, role)
		assert.Equal(t, api.RoleRef{Kind: api.KindClusterRole, Name: role}, b.RoleRef)
		assert.Equal(t, []api.Subject{groupSubject(group)}, b.Subjects)
	}
}

func TestBootstrap_TheRulesAreThoseOfADR0008AsADR0021ChangesThem(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, admin := c.clients(t, addr, adminID)
	scheduler, err := admin.GetClusterRole(ctx(t), "gaon:scheduler")
	require.NoError(t, err)
	controllers, err := admin.GetClusterRole(ctx(t), "gaon:controllers")
	require.NoError(t, err)
	adminRole, err := admin.GetClusterRole(ctx(t), "gaon:admin")
	require.NoError(t, err)

	read := []api.Verb{api.VerbGet, api.VerbList, api.VerbWatch}
	assert.Equal(t, []api.PolicyRule{
		{APIGroups: []string{api.Group}, Resources: []string{"microvms", "nodes", "volumes"}, Verbs: read},
		{APIGroups: []string{api.Group}, Resources: []string{"microvms/binding"}, Verbs: []api.Verb{api.VerbCreate}},
		{APIGroups: []string{api.Group}, Resources: []string{"microvms/status"}, Verbs: []api.Verb{api.VerbUpdate}},
		{APIGroups: []string{api.CoordinationGroup}, Resources: []string{"leases"}, Verbs: []api.Verb{api.VerbGet, api.VerbCreate, api.VerbUpdate}},
	}, scheduler.Rules)
	assert.Equal(t, []api.PolicyRule{
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []api.Verb{api.VerbGet, api.VerbList, api.VerbWatch, api.VerbDelete}},
		{APIGroups: []string{api.Group}, Resources: []string{"microvms"}, Verbs: []api.Verb{api.VerbCreate, api.VerbUpdate}},
		{APIGroups: []string{api.Group}, Resources: []string{"microvms/status", "nodes/status", "volumes/status", "certificatesigningrequests/status"}, Verbs: []api.Verb{api.VerbUpdate}},
		{APIGroups: []string{api.CoordinationGroup}, Resources: []string{"leases"}, Verbs: []api.Verb{api.VerbGet, api.VerbCreate, api.VerbUpdate}},
	}, controllers.Rules)
	assert.Equal(t, []api.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []api.Verb{api.VerbAll}}}, adminRole.Rules)
}

func TestBootstrap_NeverOverwritesAnObjectThatExistsAndCreatesOneThatIsMissing(t *testing.T) {
	c := newCluster(t)
	addr, stop := c.start(t, apiserver.ModeRBAC)
	_, admin := c.clients(t, addr, adminID)
	edited, err := admin.GetClusterRole(ctx(t), "gaon:scheduler")
	require.NoError(t, err)
	edited.Rules = []api.PolicyRule{rule(api.Group, "microvms", api.VerbGet)}
	edited, err = admin.PutClusterRole(ctx(t), edited)
	require.NoError(t, err)
	retargeted, err := admin.GetClusterRoleBinding(ctx(t), "gaon:scheduler")
	require.NoError(t, err)
	retargeted.Subjects = append(retargeted.Subjects, userSubject("extra"))
	retargeted, err = admin.PutClusterRoleBinding(ctx(t), retargeted)
	require.NoError(t, err)
	gone, err := admin.GetClusterRoleBinding(ctx(t), "gaon:controllers")
	require.NoError(t, err)
	_, err = admin.DeleteClusterRoleBinding(ctx(t), "gaon:controllers", gone.Metadata.ResourceVersion)
	require.NoError(t, err)
	require.NoError(t, stop())

	addr, _ = c.start(t, apiserver.ModeRBAC)
	_, admin = c.clients(t, addr, adminID)

	role, err := admin.GetClusterRole(ctx(t), "gaon:scheduler")
	require.NoError(t, err)
	assert.Equal(t, edited, role, "an edited role survives a restart")
	binding, err := admin.GetClusterRoleBinding(ctx(t), "gaon:scheduler")
	require.NoError(t, err)
	assert.Equal(t, retargeted, binding, "an edited binding survives a restart")
	recreated, err := admin.GetClusterRoleBinding(ctx(t), "gaon:controllers")
	require.NoError(t, err, "control: a missing binding is created at the next start")
	assert.Equal(t, []api.Subject{groupSubject("gaon:controllers")}, recreated.Subjects)
}

func TestBootstrap_AlwaysAllowCreatesNothing(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeAlwaysAllow)
	_, rbac := c.clients(t, addr, adminID)

	list, err := rbac.ListClusterRoles(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, list.Items)
	bindings, err := rbac.ListClusterRoleBindings(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, bindings.Items)
}

func TestBootstrap_TheSchedulerAndTheControllersUseLeasesAndTheControllersReachACustomResource(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	coordination := func(w who) *api.Client {
		return api.NewClient("https://"+addr+coordinationAPIRoot, adminHTTP(t, c, w))
	}

	for name, w := range map[string]who{"scheduler": schedulerID, "controllers": controllersID} {
		cl := coordination(w)
		l, err := cl.PutCoordinationLease(ctx(t), coordinationLease(name, name))
		require.NoError(t, err, name+" creates its Lease")
		_, err = cl.GetCoordinationLease(ctx(t), name)
		require.NoError(t, err, name+" reads it")
		renew := *l
		renew.Spec.RenewTime = renew.Spec.RenewTime.Add(time.Second)
		_, err = cl.PutCoordinationLease(ctx(t), &renew)
		require.NoError(t, err, name+" renews it")
		cur, err := cl.GetCoordinationLease(ctx(t), name)
		require.NoError(t, err)
		_, err = cl.DeleteCoordinationLease(ctx(t), name, cur.Metadata.ResourceVersion)
		if name == "scheduler" {
			assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler holds no delete on Leases")
		} else {
			assert.NoError(t, err, "control: the controllers' general delete reaches Leases")
		}
	}
	_, err := coordination(strangerID).PutCoordinationLease(ctx(t), coordinationLease("stranger", "s"))
	assert.ErrorIs(t, err, api.ErrForbidden, "control: a caller without the role")

	extensions := api.NewClient("https://"+addr+extensionsAPIRoot, adminHTTP(t, c, adminID))
	_, err = extensions.PutCustomResourceDefinition(ctx(t), widgetDefinition(t, false))
	require.NoError(t, err)
	admin, err := api.NewCustomResourceClient("https://"+addr, adminHTTP(t, c, adminID), widgetType)
	require.NoError(t, err)
	made, err := admin.Put(ctx(t), widget("w1", `{"size":1}`))
	require.NoError(t, err)
	ctrl, err := api.NewCustomResourceClient("https://"+addr, adminHTTP(t, c, controllersID), widgetType)
	require.NoError(t, err)
	stranger, err := api.NewCustomResourceClient("https://"+addr, adminHTTP(t, c, strangerID), widgetType)
	require.NoError(t, err)
	sched, err := api.NewCustomResourceClient("https://"+addr, adminHTTP(t, c, schedulerID), widgetType)
	require.NoError(t, err)

	_, err = ctrl.Get(ctx(t), "w1")
	assert.NoError(t, err, "the controllers get a custom object")
	_, err = ctrl.List(ctx(t))
	assert.NoError(t, err, "and list them")
	_, err = stranger.Get(ctx(t), "w1")
	assert.ErrorIs(t, err, api.ErrForbidden, "control: a caller without the role")
	_, err = sched.Delete(ctx(t), "w1", made.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler does not delete one")
	_, err = ctrl.Delete(ctx(t), "w1", made.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: the controllers do")
}

package apiserver_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// selectorFixture is an RBAC server where the user webber holds every verb on
// microvms, and update on microvms/status, only for objects labeled app=web.
// A web MicroVM and a db MicroVM are stored.
type selectorFixture struct {
	c      *cluster
	addr   string
	admin  *api.Client
	webber *api.Client
	web    *api.MicroVM
	db     *api.MicroVM
}

var allVerbs = []api.Verb{api.VerbGet, api.VerbList, api.VerbWatch, api.VerbCreate, api.VerbUpdate, api.VerbDelete}

func newSelectorFixture(t *testing.T) *selectorFixture {
	t.Helper()
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &selectorFixture{c: c, addr: addr}
	var adminRBAC *api.Client
	f.admin, adminRBAC = c.clients(t, addr, adminID)
	f.webber, _ = c.clients(t, addr, who{"webber", nil})
	selected := func(r api.PolicyRule) api.PolicyRule { r.LabelSelector = "app=web"; return r }
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("web-only",
		selected(rule(api.Group, "microvms", allVerbs...)),
		selected(rule(api.Group, "microvms/status", api.VerbUpdate))))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("web-only", "web-only", userSubject("webber")))
	require.NoError(t, err)
	f.web, err = f.admin.PutMicroVM(ctx(t), labeled(microvm("web"), "app", "web"))
	require.NoError(t, err)
	f.db, err = f.admin.PutMicroVM(ctx(t), labeled(microvm("db"), "app", "db"))
	require.NoError(t, err)
	return f
}

// withSelector is the client of the caller who for lists and watches that ask
// for text.
func (f *selectorFixture) withSelector(t *testing.T, w who, text string) *api.Client {
	t.Helper()
	certPEM, keyPEM := f.c.clientCA.issueFor(t, w)
	hc, err := api.NewTLSClient(f.c.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	return api.NewClient("https://"+f.addr+apiRoot, hc, api.WithLabelSelector(mustSelector(t, text)))
}

func TestSelectorRule_GetIsJudgedOnTheStoredLabels(t *testing.T) {
	f := newSelectorFixture(t)

	_, err := f.webber.GetMicroVM(ctx(t), "db")
	assert.ErrorIs(t, err, api.ErrForbidden, "an object outside the selector")
	_, err = f.webber.GetMicroVM(ctx(t), "web")
	assert.NoError(t, err, "control: an object inside it")
	_, err = f.webber.GetMicroVM(ctx(t), "missing")
	assert.ErrorIs(t, err, api.ErrForbidden, "a missing object counts as having no labels")
}

func TestSelectorRule_ListAndWatchNeedASelectorWithinTheRules(t *testing.T) {
	f := newSelectorFixture(t)
	webber := who{"webber", nil}
	list, err := f.admin.ListMicroVMs(ctx(t))
	require.NoError(t, err)

	_, err = f.webber.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "list without a selector asks for more than the rule allows")
	_, err = f.withSelector(t, webber, "app in (web,db)").ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "list with a wider selector")
	got, err := f.withSelector(t, webber, "app=web").ListMicroVMs(ctx(t))
	require.NoError(t, err, "control: list with the rule's selector")
	require.Len(t, got.Items, 1)
	assert.Equal(t, "web", got.Items[0].Metadata.Name)
	_, err = f.withSelector(t, webber, "app=web,tier=a").ListMicroVMs(ctx(t))
	assert.NoError(t, err, "control: list with a narrower selector")

	_, err = f.webber.WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "watch without a selector")
	_, err = f.withSelector(t, webber, "app in (web,db)").WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "watch with a wider selector")
	w, err := f.withSelector(t, webber, "app=web").WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err, "control: watch with the rule's selector")
	w.Close()
}

func TestSelectorRule_CreateIsJudgedOnTheLabelsOfTheBody(t *testing.T) {
	f := newSelectorFixture(t)

	_, err := f.webber.PutMicroVM(ctx(t), labeled(microvm("new-db"), "app", "db"))
	assert.ErrorIs(t, err, api.ErrForbidden, "labels outside the selector")
	_, err = f.webber.PutMicroVM(ctx(t), microvm("new-bare"))
	assert.ErrorIs(t, err, api.ErrForbidden, "no labels")
	_, err = f.webber.PutMicroVM(ctx(t), labeled(microvm("new-web"), "app", "web"))
	assert.NoError(t, err, "control: labels inside it")
}

func TestSelectorRule_UpdateIsJudgedOnTheStoredAndTheRequestLabels(t *testing.T) {
	f := newSelectorFixture(t)

	outside := *f.db
	outside.Spec.RunStrategy = api.RunHalted
	_, err := f.webber.PutMicroVM(ctx(t), &outside)
	assert.ErrorIs(t, err, api.ErrForbidden, "stored labels outside the selector")

	enters := *f.db
	enters.Metadata.Labels = map[string]string{"app": "web"}
	_, err = f.webber.PutMicroVM(ctx(t), &enters)
	assert.ErrorIs(t, err, api.ErrForbidden, "stored labels outside the selector, request labels inside it")

	moves := *f.web
	moves.Metadata.Labels = map[string]string{"app": "db"}
	_, err = f.webber.PutMicroVM(ctx(t), &moves)
	assert.ErrorIs(t, err, api.ErrForbidden, "request labels outside the selector, on an object stored inside it")

	stays := *f.web
	stays.Spec.RunStrategy = api.RunHalted
	_, err = f.webber.PutMicroVM(ctx(t), &stays)
	assert.NoError(t, err, "control: stored and request labels inside it")
}

func TestSelectorRule_DeleteIsJudgedOnTheStoredLabels(t *testing.T) {
	f := newSelectorFixture(t)

	_, err := f.webber.DeleteMicroVM(ctx(t), "db", f.db.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "an object outside the selector")
	_, err = f.webber.DeleteMicroVM(ctx(t), "web", f.web.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: an object inside it")
}

func TestSelectorRule_ASubresourceIsJudgedOnTheStoredLabels(t *testing.T) {
	f := newSelectorFixture(t)

	outside := *f.db
	outside.Status.Phase = api.MicroVMPending
	_, err := f.webber.PutMicroVMStatus(ctx(t), &outside)
	assert.ErrorIs(t, err, api.ErrForbidden, "status of an object outside the selector")

	inside := *f.web
	inside.Status.Phase = api.MicroVMPending
	_, err = f.webber.PutMicroVMStatus(ctx(t), &inside)
	assert.NoError(t, err, "control: status of an object inside it")
}

func TestSelectorRule_ARuleWithoutOneAllowsEveryObjectAndRulesAreNotCombined(t *testing.T) {
	f := newSelectorFixture(t)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("db-only", api.PolicyRule{
		APIGroups: []string{api.Group}, Resources: []string{"microvms"}, Verbs: []api.Verb{api.VerbList}, LabelSelector: "app=db"}))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("db-only", "db-only", userSubject("webber")))
	require.NoError(t, err)

	_, err = f.withSelector(t, who{"webber", nil}, "app in (web,db)").ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "two selector rules are judged one at a time, not as their union")

	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("all-get", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("all-get", "all-get", userSubject("webber")))
	require.NoError(t, err)
	_, err = f.webber.GetMicroVM(ctx(t), "db")
	assert.NoError(t, err, "control: a rule without a selector allows an object outside the other rule's")
}

func TestSelectorRule_ACustomKindIsJudgedLikeABuiltInOne(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	extensions := api.NewClient("https://"+addr+extensionsAPIRoot, adminHTTP(t, c, adminID))
	_, err := extensions.PutCustomResourceDefinition(ctx(t), widgetDefinition(t, false))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("widget-red", api.PolicyRule{
		APIGroups: []string{appsGroup}, Resources: []string{"widgets"}, Verbs: []api.Verb{api.VerbGet}, LabelSelector: "color=red"}))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("widget-red", "widget-red", userSubject("webber")))
	require.NoError(t, err)

	adminW, err := api.NewCustomResourceClient("https://"+addr, adminHTTP(t, c, adminID), widgetType)
	require.NoError(t, err)
	red := widget("red", `{"size":1}`)
	red.Metadata.Labels = map[string]string{"color": "red"}
	_, err = adminW.Put(ctx(t), red)
	require.NoError(t, err)
	_, err = adminW.Put(ctx(t), widget("plain", `{"size":1}`))
	require.NoError(t, err)

	webberW, err := api.NewCustomResourceClient("https://"+addr, adminHTTP(t, c, who{"webber", nil}), widgetType)
	require.NoError(t, err)
	_, err = webberW.Get(ctx(t), "plain")
	assert.ErrorIs(t, err, api.ErrForbidden, "a custom object outside the selector")
	_, err = webberW.Get(ctx(t), "red")
	assert.NoError(t, err, "control: one inside it")
}

func adminHTTP(t *testing.T, c *cluster, w who) *http.Client {
	t.Helper()
	certPEM, keyPEM := c.clientCA.issueFor(t, w)
	hc, err := api.NewTLSClient(c.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	return hc
}

func TestSelectorRule_ANodeHoldingOneDoesNotGainAFullUpdate(t *testing.T) {
	f := newSelectorFixture(t)
	_, adminRBAC := f.c.clients(t, f.addr, adminID)
	_, err := adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("web-only-node", "web-only", userSubject("node-1")))
	require.NoError(t, err)
	node1, _ := f.c.clients(t, f.addr, nodeID("node-1"))

	outside := *f.db
	outside.Spec.RunStrategy = api.RunHalted
	_, err = node1.PutMicroVM(ctx(t), &outside)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node's selector rule does not reach an object outside it")

	inside := *f.web
	inside.Spec.RunStrategy = api.RunHalted
	_, err = node1.PutMicroVM(ctx(t), &inside)
	assert.NoError(t, err, "control: it does reach one inside it")
}

// newSelectorEditor is an RBAC server where the user editor may create
// clusterroles and holds get on microvms for held, the selector it names.
func newSelectorEditor(t *testing.T, held string) (editor, admin *api.Client) {
	t.Helper()
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	_, editorRBAC := c.clients(t, addr, who{"editor", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("editor",
		rule(api.RBACGroup, "clusterroles", api.VerbCreate),
		api.PolicyRule{APIGroups: []string{api.Group}, Resources: []string{"microvms"}, Verbs: []api.Verb{api.VerbGet}, LabelSelector: held}))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("editor", "editor", userSubject("editor")))
	require.NoError(t, err)
	return editorRBAC, adminRBAC
}

func microvmGet(selector string) api.PolicyRule {
	return api.PolicyRule{APIGroups: []string{api.Group}, Resources: []string{"microvms"}, Verbs: []api.Verb{api.VerbGet}, LabelSelector: selector}
}

func TestEscalation_ASelectorRuleIsHeldOnlyWithinTheSelectorHeld(t *testing.T) {
	editor, _ := newSelectorEditor(t, "app=web")

	for name, selector := range map[string]string{
		"no selector":      "",
		"another value":    "app=db",
		"a wider selector": "app in (web,db)",
	} {
		_, err := editor.PutClusterRole(ctx(t), clusterRole("grant-"+name, microvmGet(selector)))
		assert.ErrorIs(t, err, api.ErrForbidden, name)
	}
	_, err := editor.PutClusterRole(ctx(t), clusterRole("grant-same", microvmGet("app=web")))
	assert.NoError(t, err, "control: the selector held")
	_, err = editor.PutClusterRole(ctx(t), clusterRole("grant-narrower", microvmGet("app=web,tier=a")))
	assert.NoError(t, err, "control: a selector inside the one held")
}

func TestEscalation_ARuleWithoutASelectorHoldsEverySelector(t *testing.T) {
	editor, _ := newSelectorEditor(t, "")

	_, err := editor.PutClusterRole(ctx(t), clusterRole("grant-db", microvmGet("app=db")))
	assert.NoError(t, err, "a caller who holds get without a selector may grant it with one")
	_, err = editor.PutClusterRole(ctx(t), clusterRole("grant-all", microvmGet("")))
	assert.NoError(t, err, "and without one")
}

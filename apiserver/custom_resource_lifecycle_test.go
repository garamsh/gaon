package apiserver_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

func TestDeleteCustomResourceDefinition_RemovesItsObjectsEndsTheirStreamsAndFreesTheName(t *testing.T) {
	e := newEnv(t)
	ext := e.extensions()
	created := e.define(t, widgetDefinition(t, false))
	e.define(t, definition(t, gadgetType, widgetSchema, false))
	widgets, gadgets := e.custom(t, widgetType), e.custom(t, gadgetType)
	for _, n := range []string{"w1", "w2"} {
		_, err := widgets.Put(ctx(t), widget(n, `{"size":1}`))
		require.NoError(t, err)
	}
	_, err := gadgets.Put(ctx(t), object(gadgetType, "g1", `{"size":1}`))
	require.NoError(t, err)
	wlist, err := widgets.List(ctx(t))
	require.NoError(t, err)
	objects, err := widgets.Watch(ctx(t), wlist.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer objects.Close()
	dlist, err := ext.ListCustomResourceDefinitions(ctx(t))
	require.NoError(t, err)
	defs, err := ext.WatchCustomResourceDefinitions(ctx(t), dlist.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer defs.Close()

	_, err = ext.DeleteCustomResourceDefinition(ctx(t), created.Metadata.Name, "1")
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = ext.DeleteCustomResourceDefinition(ctx(t), created.Metadata.Name, created.Metadata.ResourceVersion)
	require.NoError(t, err)

	first := next(t, defs)
	assert.Equal(t, api.EventModified, first.Type, "the definition first goes terminating")
	assert.NotNil(t, first.Object.Metadata.DeletionTimestamp)
	assert.Equal(t, api.ConditionFalse, condition(t, &first.Object, api.CRDEstablished).Status)
	last := next(t, defs)
	assert.Equal(t, api.EventDeleted, last.Type)
	deleted := map[string]bool{}
	for range 2 {
		ev := next(t, objects)
		assert.Equal(t, api.EventDeleted, ev.Type)
		deleted[ev.Object.Metadata.Name] = true
	}
	assert.Equal(t, map[string]bool{"w1": true, "w2": true}, deleted, "every object is deleted")
	ended(t, objects)

	_, err = widgets.List(ctx(t))
	assert.ErrorIs(t, err, api.ErrNotFound, "no path is served")
	_, err = ext.GetCustomResourceDefinition(ctx(t), created.Metadata.Name)
	assert.ErrorIs(t, err, api.ErrNotFound)
	_, err = gadgets.Get(ctx(t), "g1")
	assert.NoError(t, err, "control: another definition's objects stay")

	e.define(t, widgetDefinition(t, false))
	again, err := widgets.List(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, again.Items, "a recreated definition starts with no objects")
}

func TestDeleteCustomResourceDefinition_AnObjectHeldByAFinalizerKeepsItTerminatingAndRefusesCreates(t *testing.T) {
	e := newEnv(t)
	ext := e.extensions()
	created := e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	held := widget("held", `{"size":1}`)
	held.Metadata.Finalizers = []string{"example.com/cleanup"}
	heldCreated, err := widgets.Put(ctx(t), held)
	require.NoError(t, err)

	_, err = ext.DeleteCustomResourceDefinition(ctx(t), created.Metadata.Name, created.Metadata.ResourceVersion)
	require.NoError(t, err)

	got, err := ext.GetCustomResourceDefinition(ctx(t), created.Metadata.Name)
	require.NoError(t, err, "the definition stays while an object does")
	assert.NotNil(t, got.Metadata.DeletionTimestamp)
	assert.Equal(t, api.ConditionFalse, condition(t, got, api.CRDEstablished).Status)
	_, err = widgets.Put(ctx(t), widget("new", `{"size":1}`))
	assert.ErrorIs(t, err, api.ErrConflict, "a terminating kind takes no new object")
	obj, err := widgets.Get(ctx(t), "held")
	require.NoError(t, err, "control: its objects are still served")
	assert.NotNil(t, obj.Metadata.DeletionTimestamp)

	cleared := *obj
	cleared.Metadata.Finalizers = nil
	_, err = widgets.Put(ctx(t), &cleared)
	require.NoError(t, err)
	_, err = ext.GetCustomResourceDefinition(ctx(t), created.Metadata.Name)
	assert.ErrorIs(t, err, api.ErrNotFound, "the last object's removal finishes the definition")
	assert.NotEmpty(t, heldCreated.Metadata.ResourceVersion)
}

func TestDeleteCustomResourceDefinition_ARepeatedDeleteOfATerminatingDefinitionIsAccepted(t *testing.T) {
	e := newEnv(t)
	ext := e.extensions()
	created := e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	held := widget("held", `{"size":1}`)
	held.Metadata.Finalizers = []string{"example.com/cleanup"}
	_, err := widgets.Put(ctx(t), held)
	require.NoError(t, err)
	_, err = ext.DeleteCustomResourceDefinition(ctx(t), created.Metadata.Name, created.Metadata.ResourceVersion)
	require.NoError(t, err)

	_, err = ext.DeleteCustomResourceDefinition(ctx(t), created.Metadata.Name, "1")
	assert.NoError(t, err, "the version is not checked once the definition is terminating")
	_, err = ext.DeleteCustomResourceDefinition(ctx(t), "missing."+appsGroup, "1")
	assert.ErrorIs(t, err, api.ErrNotFound, "control: a missing one")
}

func TestCustomResourceDefinition_ObjectsAndDefinitionsSurviveARestart(t *testing.T) {
	c := newCluster(t)
	addr, stop := c.start(t, apiserver.ModeRBAC)
	admin, adminExt := customClients(t, c, addr, adminID, widgetType)
	_, err := adminExt.PutCustomResourceDefinition(ctx(t), widgetDefinition(t, true))
	require.NoError(t, err)
	created, err := admin.Put(ctx(t), widget("w1", `{"size":2}`))
	require.NoError(t, err)
	require.NoError(t, stop())

	addr, _ = c.start(t, apiserver.ModeRBAC)
	admin, adminExt = customClients(t, c, addr, adminID, widgetType)
	got, err := admin.Get(ctx(t), "w1")
	require.NoError(t, err, "the definition is read from the store and its path is served")
	assert.Equal(t, created.Metadata.ResourceVersion, got.Metadata.ResourceVersion)
	d, err := adminExt.GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
	require.NoError(t, err)
	assert.Equal(t, api.ConditionTrue, condition(t, d, api.CRDEstablished).Status)
	_, err = admin.PutStatus(ctx(t), got)
	assert.NoError(t, err, "the status subresource is still on")
}

func TestCustomResourceDefinition_ObjectCreatesRacingTheDeleteLeaveNoObjectOfAMissingDefinition(t *testing.T) {
	e := newEnv(t)
	ext := e.extensions()
	created := e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := string(rune('a'+i%26)) + "x"
			_, _ = widgets.Put(ctx(t), widget(name, `{"size":1}`))
		}()
	}
	_, err := ext.DeleteCustomResourceDefinition(ctx(t), created.Metadata.Name, created.Metadata.ResourceVersion)
	require.NoError(t, err)
	wg.Wait()

	_, err = ext.GetCustomResourceDefinition(ctx(t), created.Metadata.Name)
	require.ErrorIs(t, err, api.ErrNotFound)
	e.define(t, widgetDefinition(t, false))
	list, err := widgets.List(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, list.Items, "no create survived the definition it raced")
}

// customClients returns the custom-object client of the kind ct and the
// definition client of a caller whose certificate has the subject w.
func customClients(t *testing.T, c *cluster, addr string, w who, ct api.CustomResourceType) (*api.CustomResourceClient, *api.Client) {
	t.Helper()
	certPEM, keyPEM := c.clientCA.issueFor(t, w)
	hc, err := api.NewTLSClient(c.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	custom, err := api.NewCustomResourceClient("https://"+addr, hc, ct)
	require.NoError(t, err)
	return custom, api.NewClient("https://"+addr+extensionsAPIRoot, hc)
}

func TestRBAC_CustomObjectsAreAuthorizedByGroupResourceAndVerb(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminExt := customClients(t, c, addr, adminID, widgetType)
	_, adminRBAC := c.clients(t, addr, adminID)
	_, err := adminExt.PutCustomResourceDefinition(ctx(t), widgetDefinition(t, true))
	require.NoError(t, err, "gaon:admin covers the definitions")
	_, err = admin.Put(ctx(t), widget("w1", `{"size":1}`))
	require.NoError(t, err, "and the objects of a custom group")

	grant := func(name string, rules ...api.PolicyRule) {
		_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole(name, rules...))
		require.NoError(t, err)
		_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding(name, name, userSubject(name)))
		require.NoError(t, err)
	}
	grant("reader", rule(appsGroup, "widgets", api.VerbGet, api.VerbList))
	grant("other-group", rule("other.gaon.garam.sh", "widgets", api.VerbGet, api.VerbList, api.VerbWatch, api.VerbCreate, api.VerbUpdate, api.VerbDelete))
	grant("status-writer", rule(appsGroup, "widgets/status", api.VerbUpdate))

	reader, _ := customClients(t, c, addr, who{"reader", nil}, widgetType)
	_, err = reader.Get(ctx(t), "w1")
	assert.NoError(t, err, "control: get on widgets")
	_, err = reader.List(ctx(t))
	assert.NoError(t, err, "control: list on widgets")
	_, err = reader.Watch(ctx(t), "1")
	assert.ErrorIs(t, err, api.ErrForbidden, "watch is its own verb")
	_, err = reader.Put(ctx(t), widget("w2", `{"size":1}`))
	assert.ErrorIs(t, err, api.ErrForbidden, "create is its own verb")
	_, err = reader.Delete(ctx(t), "w1", "1")
	assert.ErrorIs(t, err, api.ErrForbidden, "delete is its own verb")

	other, _ := customClients(t, c, addr, who{"other-group", nil}, widgetType)
	_, err = other.Get(ctx(t), "w1")
	assert.ErrorIs(t, err, api.ErrForbidden, "a role over another group grants nothing in this one")

	w1, err := admin.Get(ctx(t), "w1")
	require.NoError(t, err)
	statuser, _ := customClients(t, c, addr, who{"status-writer", nil}, widgetType)
	_, err = statuser.PutStatus(ctx(t), w1)
	assert.NoError(t, err, "control: update on widgets/status")
	_, err = statuser.Put(ctx(t), w1)
	assert.ErrorIs(t, err, api.ErrForbidden, "widgets/status is not widgets")
	_, err = reader.PutStatus(ctx(t), w1)
	assert.ErrorIs(t, err, api.ErrForbidden, "widgets is not widgets/status")
}

func TestRBAC_DefinitionsNeedTheCustomResourceDefinitionsResourceAndNodesAndTokensAreRefused(t *testing.T) {
	c := newCluster(t)
	c.cfg.BootstrapTokenFile = writeFile(t, "tokens", []byte(tokenA+"\n"))
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminExt := customClients(t, c, addr, adminID, widgetType)
	_, adminRBAC := c.clients(t, addr, adminID)
	_, err := adminExt.PutCustomResourceDefinition(ctx(t), widgetDefinition(t, false))
	require.NoError(t, err)
	_, err = admin.Put(ctx(t), widget("w1", `{"size":1}`))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("definer", rule(api.APIExtensionsGroup, "customresourcedefinitions", api.VerbGet, api.VerbList)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("definer", "definer", userSubject("definer")))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("widgets-only", rule(appsGroup, "widgets", api.VerbGet, api.VerbList)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("widgets-only", "widgets-only", userSubject("widgets-only")))
	require.NoError(t, err)

	_, definerExt := customClients(t, c, addr, who{"definer", nil}, widgetType)
	_, err = definerExt.GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
	assert.NoError(t, err, "control: get on customresourcedefinitions")
	_, err = definerExt.PutCustomResourceDefinition(ctx(t), definition(t, gadgetType, widgetSchema, false))
	assert.ErrorIs(t, err, api.ErrForbidden, "create is not granted")
	_, err = definerExt.DeleteCustomResourceDefinition(ctx(t), "widgets."+appsGroup, "1")
	assert.ErrorIs(t, err, api.ErrForbidden, "delete is not granted")

	_, widgetsOnlyExt := customClients(t, c, addr, who{"widgets-only", nil}, widgetType)
	_, err = widgetsOnlyExt.GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
	assert.ErrorIs(t, err, api.ErrForbidden, "a right over the objects is none over the definitions")

	node, nodeExt := customClients(t, c, addr, nodeID("node-1"), widgetType)
	_, err = node.Get(ctx(t), "w1")
	assert.ErrorIs(t, err, api.ErrForbidden, "a Node reaches nothing of a custom group")
	_, err = nodeExt.ListCustomResourceDefinitions(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "nor the definitions")

	hc, err := api.NewServerTLSClient(c.caFile)
	require.NoError(t, err)
	token, err := api.NewCustomResourceClient("https://"+addr, hc, widgetType, api.WithBearerToken(tokenA))
	require.NoError(t, err)
	_, err = token.Get(ctx(t), "w1")
	assert.ErrorIs(t, err, api.ErrForbidden, "a bearer token reaches no custom path")
	tokenExt := api.NewClient("https://"+addr+extensionsAPIRoot, hc, api.WithBearerToken(tokenA))
	_, err = tokenExt.ListCustomResourceDefinitions(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "nor a definition path")
}

func TestPutCustomObject_AnUndecodableBodyIsRefused(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	path := "/apis/" + appsGroup + "/v1alpha1/widgets/w1"

	code, _ := e.sendAbsolute(t, http.MethodPut, path, `{"apiVersion":`)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	body, err := json.Marshal(widget("w1", `{"size":1}`))
	require.NoError(t, err)
	code, _ = e.sendAbsolute(t, http.MethodPut, path, string(body))
	assert.Equal(t, http.StatusOK, code, "control: a decodable body")
}

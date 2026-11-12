package apiserver_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestPutCustomResourceDefinition_EstablishesTheKindAndOnlyThenItsPathsAreServed(t *testing.T) {
	e := newEnv(t)
	_, err := e.custom(t, widgetType).List(ctx(t))
	require.ErrorIs(t, err, api.ErrNotFound, "before the definition no path of the kind exists")

	created := e.define(t, widgetDefinition(t, false))

	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	assert.EqualValues(t, 1, created.Metadata.Generation)
	assert.Equal(t, api.ConditionTrue, condition(t, created, api.CRDNamesAccepted).Status)
	assert.Equal(t, api.ConditionTrue, condition(t, created, api.CRDEstablished).Status)
	list, err := e.custom(t, widgetType).List(ctx(t))
	require.NoError(t, err)
	assert.Equal(t, "WidgetList", list.Kind, "the list carries the listKind")
	assert.Equal(t, appsGroup+"/v1alpha1", list.APIVersion)
	assert.Empty(t, list.Items)
}

func TestPutCustomResourceDefinition_TheServerWritesTheStatusAndTheBodyCannot(t *testing.T) {
	e := newEnv(t)
	d := widgetDefinition(t, false)
	d.Status.Conditions = []api.Condition{{Type: api.CRDEstablished, Status: api.ConditionFalse}}
	created := e.define(t, d)
	assert.Equal(t, api.ConditionTrue, condition(t, created, api.CRDEstablished).Status, "a created definition's status is the server's")

	update := *created
	update.Status.Conditions = []api.Condition{{Type: api.CRDEstablished, Status: api.ConditionFalse}}
	updated, err := e.extensions().PutCustomResourceDefinition(ctx(t), &update)
	require.NoError(t, err)
	assert.Equal(t, api.ConditionTrue, condition(t, updated, api.CRDEstablished).Status, "an update keeps the stored status")

	foreign := *updated
	foreign.Status.Conditions = []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), &foreign)
	assert.ErrorIs(t, err, api.ErrInvalid, "a condition of another type is refused")
}

func TestPutCustomResourceDefinition_RefusesADefinitionThatIsNotValid(t *testing.T) {
	e := newEnv(t)
	reserved := widgetDefinition(t, false)
	reserved.Spec.Group = api.Group
	reserved.Metadata.Name = "widgets." + api.Group
	_, err := e.extensions().PutCustomResourceDefinition(ctx(t), reserved)
	assert.ErrorIs(t, err, api.ErrInvalid, "a group the core serves")

	misnamed := widgetDefinition(t, false)
	misnamed.Metadata.Name = "widget"
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), misnamed)
	assert.ErrorIs(t, err, api.ErrInvalid, "a name that is not <plural>.<group>")

	namespaced := widgetDefinition(t, false)
	namespaced.Spec.Scope = "Namespaced"
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), namespaced)
	assert.ErrorIs(t, err, api.ErrInvalid, "a scope other than Cluster")

	code, _ := e.sendAbsolute(t, http.MethodPut, extensionsAPIRoot+"/customresourcedefinitions/other.apps.gaon.garam.sh", mustJSON(t, widgetDefinition(t, false)))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a body named for another path")

	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), widgetDefinition(t, false))
	assert.NoError(t, err, "control: the valid definition")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestPutCustomResourceDefinition_CreateIsAlreadyExistsAndUpdateIsCompareAndSwap(t *testing.T) {
	e := newEnv(t)
	created := e.define(t, widgetDefinition(t, false))

	_, err := e.extensions().PutCustomResourceDefinition(ctx(t), widgetDefinition(t, false))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	update := *created
	update.Spec.Versions = append([]api.CustomResourceVersion(nil), created.Spec.Versions...)
	update.Spec.Versions[0].Subresources = &api.CustomResourceSubresources{Status: &api.StatusSubresource{}}
	updated, err := e.extensions().PutCustomResourceDefinition(ctx(t), &update)
	require.NoError(t, err, "control: the version it was read at")
	assert.EqualValues(t, 2, updated.Metadata.Generation, "a change of spec is a new generation")
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), &update)
	assert.ErrorIs(t, err, api.ErrConflict, "the same version again is stale")
}

func TestPutCustomResourceDefinition_GroupNamesAndVersionNameAreImmutableAndTheSchemaIsNot(t *testing.T) {
	e := newEnv(t)
	created := e.define(t, widgetDefinition(t, false))

	renamed := *created
	renamed.Spec.Names.Singular = "gizmo"
	_, err := e.extensions().PutCustomResourceDefinition(ctx(t), &renamed)
	assert.ErrorIs(t, err, api.ErrInvalid, "a name")

	reversioned := *created
	reversioned.Spec.Versions = append([]api.CustomResourceVersion(nil), created.Spec.Versions...)
	reversioned.Spec.Versions[0].Name = "v1beta1"
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), &reversioned)
	assert.ErrorIs(t, err, api.ErrInvalid, "the version name")

	reschemed := *created
	reschemed.Spec.Versions = append([]api.CustomResourceVersion(nil), created.Spec.Versions...)
	reschemed.Spec.Versions[0].Schema = definition(t, widgetType, `{"type":"object","properties":{"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}}`, false).Spec.Versions[0].Schema
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), &reschemed)
	assert.NoError(t, err, "control: the schema changes")
}

func TestPutCustomResourceDefinition_ALaterDefinitionWithAnEarlierOnesNameIsNotAcceptedAndServesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		kind, listKind, plural, singular string
	}{
		"kind":     {"Widget", "GizmoList", "gizmos", "gizmo"},
		"listKind": {"Gizmo", "WidgetList", "gizmos", "gizmo"},
		"singular": {"Gizmo", "GizmoList", "gizmos", "widget"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.define(t, widgetDefinition(t, false))
			later := widgetDefinition(t, false)
			later.Spec.Names = api.CustomResourceNames{Kind: tc.kind, ListKind: tc.listKind, Plural: tc.plural, Singular: tc.singular}
			later.Metadata.Name = tc.plural + "." + appsGroup

			created := e.define(t, later)

			accepted := condition(t, created, api.CRDNamesAccepted)
			assert.Equal(t, api.ConditionFalse, accepted.Status)
			assert.NotEmpty(t, accepted.Reason)
			assert.Contains(t, accepted.Message, "widgets."+appsGroup, "the message names the definition that holds the name")
			assert.Equal(t, api.ConditionFalse, condition(t, created, api.CRDEstablished).Status)
			_, err := e.custom(t, api.CustomResourceType{Group: appsGroup, Version: "v1alpha1", Kind: tc.kind, Plural: tc.plural}).List(ctx(t))
			assert.ErrorIs(t, err, api.ErrNotFound, "the later definition serves no path")
			_, err = e.custom(t, widgetType).List(ctx(t))
			assert.NoError(t, err, "the older definition keeps its names and its paths")
		})
	}
}

func TestPutCustomResourceDefinition_TheSameNamesInAnotherGroupAreAccepted(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))

	other := api.CustomResourceType{Group: "other.gaon.garam.sh", Version: "v1alpha1", Kind: "Widget", Plural: "widgets"}
	created := e.define(t, definition(t, other, widgetSchema, false))

	assert.Equal(t, api.ConditionTrue, condition(t, created, api.CRDNamesAccepted).Status)
	_, err := e.custom(t, other).List(ctx(t))
	assert.NoError(t, err)
}

func TestCustomResourceDefinition_GetListAndWatch(t *testing.T) {
	e := newEnv(t)
	ext := e.extensions()
	list, err := ext.ListCustomResourceDefinitions(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, list.Items)
	w, err := ext.WatchCustomResourceDefinitions(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	widgets := e.define(t, widgetDefinition(t, false))
	e.define(t, definition(t, gadgetType, widgetSchema, false))

	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "widgets."+appsGroup, ev.Object.Metadata.Name)
	got, err := ext.GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
	require.NoError(t, err)
	assert.Equal(t, widgets, got)
	list, err = ext.ListCustomResourceDefinitions(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, api.KindCustomResourceDefinitionList, list.Kind)
	assert.Equal(t, api.APIExtensionsGroupVersion, list.APIVersion)
	_, err = ext.GetCustomResourceDefinition(ctx(t), "missing."+appsGroup)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestPutCustomResourceDefinition_ANameHeldByNoDefinitionIsFreeToTheNextOne(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	second := definition(t, gadgetType, widgetSchema, false)
	second.Spec.Names.Singular = "widget"
	refused := e.define(t, second)
	require.Equal(t, api.ConditionFalse, condition(t, refused, api.CRDNamesAccepted).Status, "control: the second shares a singular with the first")

	third := definition(t, api.CustomResourceType{Group: appsGroup, Version: "v1alpha1", Kind: "Gizmo", Plural: "gizmos"}, widgetSchema, false)
	third.Spec.Names.Kind = "Gadget"
	created := e.define(t, third)

	assert.Equal(t, api.ConditionTrue, condition(t, created, api.CRDNamesAccepted).Status, "the second was not accepted, so its kind Gadget is free to the third")
}

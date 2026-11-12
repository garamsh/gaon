package apiserver_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestPutCustomObject_CreatesThenUpdatesOnTheCurrentVersion(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)

	created, err := widgets.Put(ctx(t), widget("w1", `{"size":3}`))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	assert.EqualValues(t, 1, created.Metadata.Generation)
	_, err = widgets.Put(ctx(t), widget("w1", `{"size":4}`))
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "a body without a version creates")

	update := *created
	update.Spec = json.RawMessage(`{"size":4}`)
	updated, err := widgets.Put(ctx(t), &update)
	require.NoError(t, err)
	assert.EqualValues(t, 2, updated.Metadata.Generation, "a change of spec is a new generation")
	assert.NotEqual(t, created.Metadata.ResourceVersion, updated.Metadata.ResourceVersion)

	labelled := *updated
	labelled.Metadata.OwnerReferences = []api.OwnerReference{{APIVersion: "v1", Kind: "Owner", Name: "o", UID: "owner-uid"}}
	relabelled, err := widgets.Put(ctx(t), &labelled)
	require.NoError(t, err)
	assert.EqualValues(t, 2, relabelled.Metadata.Generation, "a change of metadata is not")

	_, err = widgets.Put(ctx(t), &update)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
}

func TestPutCustomObject_PrunesWhatTheSchemaDoesNotNameExceptUnderAPreservingNode(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	o := widget("w1", `{"size":3,"unknown":1,"extra":{"anything":[1,{"goes":true}]}}`)
	o.Status = json.RawMessage(`{"ready":true,"bogus":"x"}`)

	created, err := widgets.Put(ctx(t), o)
	require.NoError(t, err)

	assert.JSONEq(t, `{"size":3,"extra":{"anything":[1,{"goes":true}]}}`, string(created.Spec), "unknown is pruned, extra keeps what it holds")
	assert.JSONEq(t, `{"ready":true}`, string(created.Status))
	got, err := widgets.Get(ctx(t), "w1")
	require.NoError(t, err)
	assert.JSONEq(t, string(created.Spec), string(got.Spec), "what is stored is the pruned object")
}

func TestPutCustomObject_RefusesAnObjectThatFailsTheSchema(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)

	for name, spec := range map[string]string{
		"a missing required field":  `{}`,
		"a wrong type":              `{"size":"3"}`,
		"a fraction as an integer":  `{"size":2.5}`,
		"below the minimum":         `{"size":0}`,
		"above the maximum":         `{"size":11}`,
		"a value outside the enum":  `{"size":3,"color":"green"}`,
		"a pattern mismatch":        `{"size":3,"label":"ABC"}`,
		"a non-array":               `{"size":3,"tags":"a"}`,
		"an item of the wrong type": `{"size":3,"tags":["a",1]}`,
		"a null required field":     `{"size":null}`,
	} {
		_, err := widgets.Put(ctx(t), widget("bad", spec))
		assert.ErrorIs(t, err, api.ErrInvalid, name)
	}
	for name, spec := range map[string]string{
		"the minimum":           `{"size":1}`,
		"the maximum":           `{"size":10}`,
		"a value in enum":       `{"size":3,"color":"red"}`,
		"a pattern match":       `{"size":3,"label":"abc"}`,
		"an array of items":     `{"size":3,"tags":["a","b"]}`,
		"an integer as 3.0":     `{"size":3.0}`,
		"a null optional field": `{"size":3,"color":null}`,
	} {
		_, err := widgets.Put(ctx(t), widget("good-"+name, spec))
		assert.NoError(t, err, "control: "+name)
	}
	list, err := widgets.List(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 7, "no refused object was stored")
}

func TestPutCustomObject_ASchemaChangeAppliesToTheNextWrite(t *testing.T) {
	e := newEnv(t)
	created := e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	_, err := widgets.Put(ctx(t), widget("w1", `{"size":30}`))
	require.ErrorIs(t, err, api.ErrInvalid, "above the maximum of 10")

	loosened := *created
	loosened.Spec.Versions = append([]api.CustomResourceVersion(nil), created.Spec.Versions...)
	loosened.Spec.Versions[0].Schema = definition(t, widgetType, `{"type":"object","properties":{"spec":{"type":"object","properties":{"size":{"type":"integer"}}}}}`, false).Spec.Versions[0].Schema
	_, err = e.extensions().PutCustomResourceDefinition(ctx(t), &loosened)
	require.NoError(t, err)

	_, err = widgets.Put(ctx(t), widget("w1", `{"size":30}`))
	assert.NoError(t, err)
}

func TestPutCustomObject_TheBodyMustBeOfTheKindAndNameOfThePath(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	path := "/apis/" + appsGroup + "/v1alpha1/widgets/"

	code, _ := e.sendAbsolute(t, http.MethodPut, path+"w2", mustJSON(t, widget("w1", `{"size":3}`)))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a name that is not the path's")
	gadget := object(gadgetType, "w1", `{"size":3}`)
	code, _ = e.sendAbsolute(t, http.MethodPut, path+"w1", mustJSON(t, gadget))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "another kind")
	code, _ = e.sendAbsolute(t, http.MethodPut, path+"w1", mustJSON(t, widget("w1", `{"size":3}`)))
	assert.Equal(t, http.StatusOK, code, "control: the name and the kind of the path")
}

func TestPutCustomObjectStatus_TheBodyMustBeOfTheNameOfThePath(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, true))
	created, err := e.custom(t, widgetType).Put(ctx(t), widget("w1", `{"size":3}`))
	require.NoError(t, err)
	path := "/apis/" + appsGroup + "/v1alpha1/widgets/"

	other := *created
	other.Metadata.Name = "w2"
	code, _ := e.sendAbsolute(t, http.MethodPut, path+"w1/status", mustJSON(t, &other))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a name that is not the path's")
	code, _ = e.sendAbsolute(t, http.MethodPut, path+"w1/status", mustJSON(t, created))
	assert.Equal(t, http.StatusOK, code, "control: the name of the path")
}

func TestCustomObject_OnlyThePathOfAServedKindAndVersionExists(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	base := "/apis/" + appsGroup

	for name, path := range map[string]string{
		"another version":                    base + "/v1/widgets",
		"another plural":                     base + "/v1alpha1/gadgets",
		"another group":                      "/apis/other.gaon.garam.sh/v1alpha1/widgets",
		"the singular":                       base + "/v1alpha1/widget",
		"a built-in plural of another group": "/apis/" + api.GroupVersion + "/clusterroles",
	} {
		code, _ := e.sendAbsolute(t, http.MethodGet, path, "")
		assert.Equal(t, http.StatusNotFound, code, name)
	}
	code, _ := e.sendAbsolute(t, http.MethodGet, base+"/v1alpha1/widgets", "")
	assert.Equal(t, http.StatusOK, code, "control: the served path")
}

func TestPutCustomObject_WithTheStatusSubresourceThePutLeavesStatusAndTheStatusPutLeavesTheRest(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, true))
	widgets := e.custom(t, widgetType)
	o := widget("w1", `{"size":3}`)
	o.Status = json.RawMessage(`{"ready":true}`)

	created, err := widgets.Put(ctx(t), o)
	require.NoError(t, err)
	assert.Empty(t, created.Status, "a create does not take the status")

	update := *created
	update.Spec = json.RawMessage(`{"size":4}`)
	update.Status = json.RawMessage(`{"ready":true}`)
	updated, err := widgets.Put(ctx(t), &update)
	require.NoError(t, err)
	assert.JSONEq(t, `{"size":4}`, string(updated.Spec), "control: the spec changes")
	assert.Empty(t, updated.Status, "an update ignores the status")

	report := *updated
	report.Spec = json.RawMessage(`{"size":9}`)
	report.Status = json.RawMessage(`{"ready":true,"bogus":1}`)
	reported, err := widgets.PutStatus(ctx(t), &report)
	require.NoError(t, err)
	assert.JSONEq(t, `{"size":4}`, string(reported.Spec), "the status path ignores the spec")
	assert.JSONEq(t, `{"ready":true}`, string(reported.Status), "and prunes the status")
	assert.EqualValues(t, 2, reported.Metadata.Generation, "a status write is not a new generation")

	_, err = widgets.PutStatus(ctx(t), &report)
	assert.ErrorIs(t, err, api.ErrConflict, "stale: the status write moved the version")
	bad := *reported
	bad.Status = json.RawMessage(`{"ready":"yes"}`)
	_, err = widgets.PutStatus(ctx(t), &bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "a status that fails the schema")
}

func TestPutCustomObject_WithoutTheStatusSubresourceThePutWritesStatusAndThereIsNoStatusPath(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	o := widget("w1", `{"size":3}`)
	o.Status = json.RawMessage(`{"ready":true}`)

	created, err := widgets.Put(ctx(t), o)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ready":true}`, string(created.Status), "the object's PUT carries the status")
	_, err = widgets.PutStatus(ctx(t), created)
	assert.ErrorIs(t, err, api.ErrNotFound, "no /status path")
}

func TestCustomObject_GetAndListHoldOnlyTheKindsOwnObjects(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	e.define(t, definition(t, gadgetType, widgetSchema, false))
	widgets, gadgets := e.custom(t, widgetType), e.custom(t, gadgetType)
	w1, err := widgets.Put(ctx(t), widget("w1", `{"size":1}`))
	require.NoError(t, err)
	_, err = widgets.Put(ctx(t), widget("w2", `{"size":2}`))
	require.NoError(t, err)
	_, err = gadgets.Put(ctx(t), object(gadgetType, "g1", `{"size":1}`))
	require.NoError(t, err)

	got, err := widgets.Get(ctx(t), "w1")
	require.NoError(t, err)
	assert.Equal(t, w1, got)
	list, err := widgets.List(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 2, "the gadget is not a widget")
	assert.NotEmpty(t, list.Metadata.ResourceVersion)
	_, err = widgets.Get(ctx(t), "g1")
	assert.ErrorIs(t, err, api.ErrNotFound)
	_, err = gadgets.Get(ctx(t), "g1")
	assert.NoError(t, err, "control: the gadget is a gadget")
}

func TestCustomObject_AWatchSeesWhatHappensAfterTheListedVersion(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	_, err := widgets.Put(ctx(t), widget("before", `{"size":1}`))
	require.NoError(t, err)
	list, err := widgets.List(ctx(t))
	require.NoError(t, err)
	w, err := widgets.Watch(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	added, err := widgets.Put(ctx(t), widget("after", `{"size":2}`))
	require.NoError(t, err)
	changed := *added
	changed.Spec = json.RawMessage(`{"size":3}`)
	_, err = widgets.Put(ctx(t), &changed)
	require.NoError(t, err)

	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "after", ev.Object.Metadata.Name, "nothing listed is replayed")
	ev = next(t, w)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.JSONEq(t, `{"size":3}`, string(ev.Object.Spec))
}

func TestDeleteCustomObject_WithoutFinalizersRemovesItAndWithOneSetsTheDeletionTimestamp(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	plain, err := widgets.Put(ctx(t), widget("plain", `{"size":1}`))
	require.NoError(t, err)
	held := widget("held", `{"size":1}`)
	held.Metadata.Finalizers = []string{"example.com/cleanup"}
	heldCreated, err := widgets.Put(ctx(t), held)
	require.NoError(t, err)

	_, err = widgets.Delete(ctx(t), "plain", "1")
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = widgets.Delete(ctx(t), "plain", plain.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = widgets.Get(ctx(t), "plain")
	assert.ErrorIs(t, err, api.ErrNotFound, "no finalizer, removed")

	marked, err := widgets.Delete(ctx(t), "held", heldCreated.Metadata.ResourceVersion)
	require.NoError(t, err)
	require.NotNil(t, marked.Metadata.DeletionTimestamp)
	got, err := widgets.Get(ctx(t), "held")
	require.NoError(t, err, "a finalizer keeps it")
	assert.NotNil(t, got.Metadata.DeletionTimestamp)
	again, err := widgets.Delete(ctx(t), "held", got.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Equal(t, got.Metadata.ResourceVersion, again.Metadata.ResourceVersion, "a second delete changes nothing")
}

func TestPutCustomObject_ClearingTheLastFinalizerOfADeletedObjectRemovesItAndANewOneIsRefused(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	o := widget("held", `{"size":1}`)
	o.Metadata.Finalizers = []string{"example.com/a", "example.com/b"}
	created, err := widgets.Put(ctx(t), o)
	require.NoError(t, err)
	list, err := widgets.List(ctx(t))
	require.NoError(t, err)
	w, err := widgets.Watch(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	marked, err := widgets.Delete(ctx(t), "held", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Equal(t, api.EventModified, next(t, w).Type, "the deletion timestamp is a Modified")

	added := *marked
	added.Metadata.Finalizers = []string{"example.com/a", "example.com/b", "example.com/c"}
	_, err = widgets.Put(ctx(t), &added)
	assert.ErrorIs(t, err, api.ErrInvalid, "a finalizer cannot be added to an object being deleted")
	one := *marked
	one.Metadata.Finalizers = []string{"example.com/b"}
	left, err := widgets.Put(ctx(t), &one)
	require.NoError(t, err, "control: a finalizer is removed")
	_, err = widgets.Get(ctx(t), "held")
	require.NoError(t, err, "one finalizer is left")

	none := *left
	none.Metadata.Finalizers = nil
	_, err = widgets.Put(ctx(t), &none)
	require.NoError(t, err)
	_, err = widgets.Get(ctx(t), "held")
	assert.ErrorIs(t, err, api.ErrNotFound)
	assert.Equal(t, api.EventModified, next(t, w).Type)
	ev := next(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type, "Deleted once the finalizers are gone")
	assert.Equal(t, "held", ev.Object.Metadata.Name)
}

func TestCustomObject_AKeyBeneathTheKindsPrefixIsNotAnObjectAndEndsAWatch(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	widgets := e.custom(t, widgetType)
	_, err := widgets.Put(ctx(t), widget("w1", `{"size":1}`))
	require.NoError(t, err)
	list, err := widgets.List(ctx(t))
	require.NoError(t, err)
	w, err := widgets.Watch(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	_, err = e.etcd.Put(ctx(t), "/registry/widgets."+appsGroup+"/removed/nested", "{}")
	require.NoError(t, err)

	ended(t, w)
	list, err = widgets.List(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 1, "the key is no object")
	_, err = widgets.Get(ctx(t), "w1")
	assert.NoError(t, err, "control: the object is still there")
}

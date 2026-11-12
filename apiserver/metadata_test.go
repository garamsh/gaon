package apiserver_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func labeled(m *api.MicroVM, kv ...string) *api.MicroVM {
	m.Metadata.Labels = map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m.Metadata.Labels[kv[i]] = kv[i+1]
	}
	return m
}

func mustSelector(t *testing.T, text string) api.Selector {
	t.Helper()
	sel, err := api.ParseSelector(text)
	require.NoError(t, err)
	return sel
}

func TestUID_TheServerSetsItAtCreateAndReplacesOneTheRequestCarries(t *testing.T) {
	e := newEnv(t)
	own := microvm("own")
	created := e.createMicroVM(t, own)
	assert.NotEmpty(t, created.Metadata.UID, "the server assigns a uid")

	forged := microvm("forged")
	forged.Metadata.UID = "forged-by-the-caller"
	got := e.createMicroVM(t, forged)
	assert.NotEqual(t, api.UID("forged-by-the-caller"), got.Metadata.UID, "the request's uid is replaced")
	assert.NotEmpty(t, got.Metadata.UID)
	assert.NotEqual(t, created.Metadata.UID, got.Metadata.UID, "control: two objects, two uids")
}

func TestUID_AnUpdateCannotChangeItAndKeepsItWhenTheRequestHasNone(t *testing.T) {
	e := newEnv(t)
	created := e.createMicroVM(t, microvm("a"))

	changed := *created
	changed.Metadata.UID = "another-uid"
	_, err := e.api.PutMicroVM(ctx(t), &changed)
	assert.ErrorIs(t, err, api.ErrInvalid, "a different uid is refused")

	same := *created
	same.Metadata.UID = created.Metadata.UID
	updated, err := e.api.PutMicroVM(ctx(t), &same)
	require.NoError(t, err, "control: the stored uid is accepted")
	assert.Equal(t, created.Metadata.UID, updated.Metadata.UID)

	none := *updated
	none.Metadata.UID = ""
	kept, err := e.api.PutMicroVM(ctx(t), &none)
	require.NoError(t, err)
	assert.Equal(t, created.Metadata.UID, kept.Metadata.UID, "a request without a uid keeps the stored one")
}

func TestUID_ACustomObjectGetsOneAndKeepsIt(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	w := e.custom(t, widgetType)

	created, err := w.Put(ctx(t), widget("w1", `{"size":2}`))
	require.NoError(t, err)
	require.NotEmpty(t, created.Metadata.UID)

	changed := *created
	changed.Metadata.UID = "another-uid"
	_, err = w.Put(ctx(t), &changed)
	assert.ErrorIs(t, err, api.ErrInvalid)

	kept := *created
	kept.Metadata.UID = ""
	got, err := w.Put(ctx(t), &kept)
	require.NoError(t, err, "control: the same update without a uid")
	assert.Equal(t, created.Metadata.UID, got.Metadata.UID)
}

func TestMetadata_EveryWriteValidatesIt(t *testing.T) {
	e := newEnv(t)
	bad := labeled(microvm("bad"), "Not Valid!", "x")
	_, err := e.api.PutMicroVM(ctx(t), bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "a label key that is not valid on a MicroVM")
	_, err = e.api.PutMicroVM(ctx(t), labeled(microvm("good"), "app", "web"))
	assert.NoError(t, err, "control: a valid label")

	n := node("n1")
	n.Metadata.Labels = map[string]string{"Not Valid!": "x"}
	_, err = e.api.PutNode(ctx(t), n)
	assert.ErrorIs(t, err, api.ErrInvalid, "a label key that is not valid on a Node")

	e.define(t, widgetDefinition(t, false))
	o := widget("w1", `{"size":2}`)
	o.Metadata.Labels = map[string]string{"Not Valid!": "x"}
	_, err = e.custom(t, widgetType).Put(ctx(t), o)
	assert.ErrorIs(t, err, api.ErrInvalid, "a label key that is not valid on a custom object")
}

func TestSelector_AListAnswersOnlyTheSelectedObjectsOfABuiltInKind(t *testing.T) {
	e := newEnv(t)
	e.createMicroVM(t, labeled(microvm("web"), "app", "web"))
	e.createMicroVM(t, labeled(microvm("db"), "app", "db"))
	e.createMicroVM(t, microvm("bare"))

	selected := api.NewClient(e.base+apiRoot, nil, api.WithLabelSelector(mustSelector(t, "app=web")))
	list, err := selected.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "web", list.Items[0].Metadata.Name)
	assert.NotEmpty(t, list.Metadata.ResourceVersion)

	not := api.NewClient(e.base+apiRoot, nil, api.WithLabelSelector(mustSelector(t, "app!=web")))
	list, err = not.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 2, "!= holds for an object without the label")

	all, err := e.api.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	assert.Len(t, all.Items, 3, "control: no selector lists every object")
}

func TestSelector_AListAnswersOnlyTheSelectedObjectsOfACustomKind(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	w := e.custom(t, widgetType)
	red := widget("red", `{"size":1}`)
	red.Metadata.Labels = map[string]string{"color": "red"}
	_, err := w.Put(ctx(t), red)
	require.NoError(t, err)
	_, err = w.Put(ctx(t), widget("plain", `{"size":1}`))
	require.NoError(t, err)

	sel, err := api.NewCustomResourceClient(e.base, nil, widgetType, api.WithLabelSelector(mustSelector(t, "color in (red)")))
	require.NoError(t, err)
	list, err := sel.List(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "red", list.Items[0].Metadata.Name)
	all, err := w.List(ctx(t))
	require.NoError(t, err)
	assert.Len(t, all.Items, 2, "control: no selector lists both")
}

func TestSelector_ATextThatDoesNotParseIsInvalid(t *testing.T) {
	e := newEnv(t)
	status, _ := e.send(t, "GET", "/microvms?labelSelector=app%3D%3D%3D", "")
	assert.Equal(t, 422, status)
	status, _ = e.send(t, "GET", "/microvms?labelSelector=app%3Dweb", "")
	assert.Equal(t, 200, status, "control: a selector that parses")
}

func TestSelector_AWatchTellsWhenAnObjectStartsAndStopsToMatch(t *testing.T) {
	e := newEnv(t)
	created := e.createMicroVM(t, labeled(microvm("a"), "app", "db"))
	list, err := e.api.ListMicroVMs(ctx(t))
	require.NoError(t, err)

	selected := api.NewClient(e.base+apiRoot, nil, api.WithLabelSelector(mustSelector(t, "app=web")))
	w, err := selected.WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	// Matching neither before nor after is not sent: the next event is the
	// first one that is.
	noise := *created
	noise.Spec.RunStrategy = api.RunHalted
	noisy, err := e.api.PutMicroVM(ctx(t), &noise)
	require.NoError(t, err)

	starts := *noisy
	starts.Metadata.Labels = map[string]string{"app": "web"}
	startsOut, err := e.api.PutMicroVM(ctx(t), &starts)
	require.NoError(t, err)
	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type, "an object that starts to match is Added")
	assert.Equal(t, api.RunHalted, ev.Object.Spec.RunStrategy, "the event carries the first update, so nothing was sent before")
	assert.Equal(t, "web", ev.Object.Metadata.Labels["app"])

	while := *startsOut
	while.Spec.RunStrategy = api.RunOnce
	whileOut, err := e.api.PutMicroVM(ctx(t), &while)
	require.NoError(t, err)
	ev = next(t, w)
	assert.Equal(t, api.EventModified, ev.Type, "an object that matches before and after is Modified")

	stops := *whileOut
	stops.Metadata.Labels = map[string]string{"app": "db"}
	_, err = e.api.PutMicroVM(ctx(t), &stops)
	require.NoError(t, err)
	ev = next(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type, "an object that stops matching is Deleted")
	assert.Equal(t, "db", ev.Object.Metadata.Labels["app"], "the event carries the object as it is after the change")
}

func TestSelector_AWatchSendsDeletedForARemovedObjectThatMatchedAndNothingForOneThatDidNot(t *testing.T) {
	e := newEnv(t)
	list, err := e.api.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	selected := api.NewClient(e.base+apiRoot, nil, api.WithLabelSelector(mustSelector(t, "app=web")))
	w, err := selected.WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	other := e.createMicroVM(t, labeled(microvm("other"), "app", "db"))
	_, err = e.api.DeleteMicroVM(ctx(t), "other", other.Metadata.ResourceVersion)
	require.NoError(t, err)
	web := e.createMicroVM(t, labeled(microvm("web"), "app", "web"))
	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "web", ev.Object.Metadata.Name, "the object that never matched produced no event")
	_, err = e.api.DeleteMicroVM(ctx(t), "web", web.Metadata.ResourceVersion)
	require.NoError(t, err)
	ev = next(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type)
	assert.Equal(t, "web", ev.Object.Metadata.Name)
}

func TestSelector_AWatchOfACustomKindFiltersToo(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	w := e.custom(t, widgetType)
	list, err := w.List(ctx(t))
	require.NoError(t, err)
	sel, err := api.NewCustomResourceClient(e.base, nil, widgetType, api.WithLabelSelector(mustSelector(t, "color=red")))
	require.NoError(t, err)
	watch, err := sel.Watch(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer watch.Close()

	_, err = w.Put(ctx(t), widget("plain", `{"size":1}`))
	require.NoError(t, err)
	red := widget("red", `{"size":1}`)
	red.Metadata.Labels = map[string]string{"color": "red"}
	_, err = w.Put(ctx(t), red)
	require.NoError(t, err)
	ev := next(t, watch)
	assert.Equal(t, "red", ev.Object.Metadata.Name, "control: the matching object arrives, the other does not")
}

func TestUID_AnObjectStoredWithoutOneTakesOneOnItsNextWrite(t *testing.T) {
	e := newEnv(t)
	created := e.createMicroVM(t, microvm("old"))
	stored := *created
	stored.Metadata.UID = ""
	raw, err := json.Marshal(&stored)
	require.NoError(t, err)
	_, err = e.etcd.Put(ctx(t), "/registry/microvms/old", string(raw))
	require.NoError(t, err)
	before, err := e.api.GetMicroVM(ctx(t), "old")
	require.NoError(t, err)
	require.Empty(t, before.Metadata.UID, "the fixture is an object written before uids existed")

	before.Metadata.UID = "chosen-by-the-caller"
	got, err := e.api.PutMicroVM(ctx(t), before)
	require.NoError(t, err, "a request uid is accepted on an object stored without one")
	assert.NotEmpty(t, got.Metadata.UID, "the server gives the object a uid")
	assert.NotEqual(t, api.UID("chosen-by-the-caller"), got.Metadata.UID, "and it is the server's own")
}

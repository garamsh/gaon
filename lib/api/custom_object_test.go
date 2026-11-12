package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

var (
	widgetType = api.CustomResourceType{Group: "apps.gaon.garam.sh", Version: "v1alpha1", Kind: "Widget", Plural: "widgets"}
	gadgetType = api.CustomResourceType{Group: "apps.gaon.garam.sh", Version: "v1alpha1", Kind: "Gadget", Plural: "gadgets"}
)

func newWidget(name string) *api.CustomObject {
	return &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: "apps.gaon.garam.sh/v1alpha1", Kind: "Widget"},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     json.RawMessage(`{"size":3}`),
	}
}

func newWidgetClient(t *testing.T, ct api.CustomResourceType) *api.CustomResourceClient {
	t.Helper()
	s, _ := newMemoryServer(t)
	c, err := api.NewCustomResourceClient(s.url, nil, ct)
	require.NoError(t, err)
	return c
}

func TestCustomObjectValidateChecksIdentity(t *testing.T) {
	require.NoError(t, newWidget("w").Validate(widgetType), "control: right group, version and kind")

	o := newWidget("w")
	o.Kind = "Gadget"
	assert.ErrorIs(t, o.Validate(widgetType), api.ErrInvalid, "another kind")
	o = newWidget("w")
	o.APIVersion = "apps.gaon.garam.sh/v1"
	assert.ErrorIs(t, o.Validate(widgetType), api.ErrInvalid, "another version")
	o = newWidget("")
	assert.ErrorIs(t, o.Validate(widgetType), api.ErrInvalid, "no name")
}

func TestCustomObjectValidateRefusesASpecOrStatusThatIsNotAnObject(t *testing.T) {
	o := newWidget("w")
	o.Status = json.RawMessage(`{"ready":true}`)
	require.NoError(t, o.Validate(widgetType), "control: spec and status are objects")
	o = newWidget("w")
	o.Spec, o.Status = nil, nil
	require.NoError(t, o.Validate(widgetType), "control: both may be absent")

	o = newWidget("w")
	o.Spec = json.RawMessage(`[1]`)
	assert.ErrorIs(t, o.Validate(widgetType), api.ErrInvalid, "spec as an array")
	o = newWidget("w")
	o.Status = json.RawMessage(`"ready"`)
	assert.ErrorIs(t, o.Validate(widgetType), api.ErrInvalid, "status as a string")
}

func TestNewCustomResourceClientRefusesAnEmptyField(t *testing.T) {
	_, err := api.NewCustomResourceClient("http://x", nil, widgetType)
	require.NoError(t, err, "control: every field is set")
	for name, edit := range map[string]func(*api.CustomResourceType){
		"group":   func(c *api.CustomResourceType) { c.Group = "" },
		"version": func(c *api.CustomResourceType) { c.Version = "" },
		"kind":    func(c *api.CustomResourceType) { c.Kind = "" },
		"plural":  func(c *api.CustomResourceType) { c.Plural = "" },
	} {
		ct := widgetType
		edit(&ct)
		_, err := api.NewCustomResourceClient("http://x", nil, ct)
		assert.Error(t, err, name)
	}
}

func TestCustomResourceClientPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	c := newWidgetClient(t, widgetType)

	_, err := c.Get(ctx, "w")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.Put(ctx, newWidget("w"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	_, err = c.Put(ctx, newWidget("w"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "a create of an existing name")

	stale := *created
	stale.Metadata.ResourceVersion = "99"
	_, err = c.Put(ctx, &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "an update at a stale version")
	created.Spec = json.RawMessage(`{"size":4}`)
	updated, err := c.Put(ctx, created)
	require.NoError(t, err)
	assert.JSONEq(t, `{"size":4}`, string(updated.Spec))

	wrong := newWidget("bad")
	wrong.Kind = "Gadget"
	_, err = c.Put(ctx, wrong)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.Get(ctx, "w")
	require.NoError(t, err)
	assert.JSONEq(t, `{"size":4}`, string(got.Spec))

	list, err := c.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, "WidgetList", list.Kind)
	require.Len(t, list.Items, 1)

	_, err = c.Delete(ctx, "w", "")
	assert.ErrorContains(t, err, "resource version is empty", "a delete names the version it deletes, before any request")
	_, err = c.Delete(ctx, "w", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
}

func TestCustomResourceClientPutStatusWritesStatusOnly(t *testing.T) {
	ctx := testContext(t)
	c := newWidgetClient(t, widgetType)
	created, err := c.Put(ctx, newWidget("w"))
	require.NoError(t, err)

	next := *created
	next.Spec = json.RawMessage(`{"size":99}`)
	next.Status = json.RawMessage(`{"ready":true}`)
	written, err := c.PutStatus(ctx, &next)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ready":true}`, string(written.Status))
	assert.JSONEq(t, `{"size":3}`, string(written.Spec), "the status path leaves spec alone")

	_, err = c.PutStatus(ctx, &next)
	assert.ErrorIs(t, err, api.ErrConflict, "control: the version moved, so a second write at it conflicts")
}

func TestCustomResourceClientWatchDeliversItsKindOnly(t *testing.T) {
	ctx := testContext(t)
	s, _ := newMemoryServer(t)
	widgets, err := api.NewCustomResourceClient(s.url, nil, widgetType)
	require.NoError(t, err)
	gadgets, err := api.NewCustomResourceClient(s.url, nil, gadgetType)
	require.NoError(t, err)

	g := newWidget("g")
	g.Kind = "Gadget"
	_, err = gadgets.Put(ctx, g)
	require.NoError(t, err)
	_, err = widgets.Put(ctx, newWidget("w"))
	require.NoError(t, err)

	w, err := widgets.Watch(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "w", ev.Object.Metadata.Name, "the gadget's event is not delivered")

	_, err = widgets.Watch(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}

func TestCustomResourceClientWatchFromAnExpiredVersionIsExpired(t *testing.T) {
	ctx := testContext(t)
	s, _ := newMemoryServer(t)
	c, err := api.NewCustomResourceClient(s.url, nil, widgetType)
	require.NoError(t, err)
	_, err = c.Put(ctx, newWidget("w"))
	require.NoError(t, err)
	_, err = c.Put(ctx, newWidget("x"))
	require.NoError(t, err)
	s.compact()

	stale, err := c.Watch(ctx, "1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)
}

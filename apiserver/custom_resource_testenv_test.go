package apiserver_test

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// extensionsAPIRoot is the root of the resource-definition group, which the
// server is documented to serve.
const extensionsAPIRoot = "/apis/apiextensions.gaon.garam.sh/v1alpha1"

const appsGroup = "apps.gaon.garam.sh"

var (
	widgetType = api.CustomResourceType{Group: appsGroup, Version: "v1alpha1", Kind: "Widget", Plural: "widgets"}
	gadgetType = api.CustomResourceType{Group: appsGroup, Version: "v1alpha1", Kind: "Gadget", Plural: "gadgets"}
)

// widgetSchema names one rule of the schema vocabulary per field: required,
// minimum and maximum, enum, pattern, items and the opt-out of pruning.
const widgetSchema = `{"type":"object","properties":{
  "spec":{"type":"object","required":["size"],"properties":{
    "size":{"type":"integer","minimum":1,"maximum":10},
    "color":{"type":"string","enum":["red","blue"]},
    "label":{"type":"string","pattern":"^[a-z]+$"},
    "tags":{"type":"array","items":{"type":"string"}},
    "extra":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}},
  "status":{"type":"object","properties":{"ready":{"type":"boolean"}}}}}`

// definition returns a valid definition of the kind t whose schema is schema,
// with the status subresource where status is set.
func definition(t *testing.T, ct api.CustomResourceType, schema string, status bool) *api.CustomResourceDefinition {
	t.Helper()
	subresources := ""
	if status {
		subresources = `,"subresources":{"status":{}}`
	}
	doc := fmt.Sprintf(`{"apiVersion":%q,"kind":"CustomResourceDefinition","metadata":{"name":%q},
	  "spec":{"group":%q,"names":{"kind":%q,"listKind":%q,"plural":%q,"singular":%q},"scope":"Cluster",
	  "versions":[{"name":%q,"served":true,"storage":true,"schema":{"openAPIV3Schema":%s}%s}]}}`,
		api.APIExtensionsGroupVersion, ct.Plural+"."+ct.Group, ct.Group, ct.Kind, ct.Kind+"List", ct.Plural, strings.ToLower(ct.Kind), ct.Version, schema, subresources)
	var d api.CustomResourceDefinition
	require.NoError(t, json.Unmarshal([]byte(doc), &d))
	return &d
}

func widgetDefinition(t *testing.T, status bool) *api.CustomResourceDefinition {
	t.Helper()
	return definition(t, widgetType, widgetSchema, status)
}

// object returns a custom object of the kind ct with spec as given.
func object(ct api.CustomResourceType, name, spec string) *api.CustomObject {
	return &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: ct.Group + "/" + ct.Version, Kind: ct.Kind},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     json.RawMessage(spec),
	}
}

func widget(name, spec string) *api.CustomObject { return object(widgetType, name, spec) }

// extensions is the client of the resource-definition group on the plain HTTP
// server of e, which authorizes nobody.
func (e *env) extensions() *api.Client {
	return api.NewClient(e.base+extensionsAPIRoot, nil)
}

// custom is the client of the kind ct on the plain HTTP server of e.
func (e *env) custom(t *testing.T, ct api.CustomResourceType) *api.CustomResourceClient {
	t.Helper()
	c, err := api.NewCustomResourceClient(e.base, nil, ct)
	require.NoError(t, err)
	return c
}

// define creates d and fails the test if that does not work.
func (e *env) define(t *testing.T, d *api.CustomResourceDefinition) *api.CustomResourceDefinition {
	t.Helper()
	created, err := e.extensions().PutCustomResourceDefinition(ctx(t), d)
	require.NoError(t, err)
	return created
}

// condition returns the condition of type typ, which the test fails without.
func condition(t *testing.T, d *api.CustomResourceDefinition, typ string) api.Condition {
	t.Helper()
	for _, c := range d.Status.Conditions {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("definition %s has no %s condition: %+v", d.Metadata.Name, typ, d.Status.Conditions)
	return api.Condition{}
}

// ended fails the test unless the watch's stream ends, as it does after the
// last event of a removed definition's objects.
func ended[T any](t *testing.T, w *api.Watch[T]) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := w.Next()
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.EOF, "the stream ends and carries nothing more")
	case <-time.After(15 * time.Second):
		t.Fatal("the stream did not end")
	}
}

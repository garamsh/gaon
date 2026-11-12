package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

const widgetSchema = `{
	"type": "object",
	"properties": {
		"spec": {
			"type": "object",
			"description": "what a user wants",
			"required": ["size"],
			"properties": {
				"size": {"type": "integer", "minimum": 1, "maximum": 10},
				"color": {"type": "string", "enum": ["red", "blue"]},
				"name": {"type": "string", "pattern": "^[a-z]+$"},
				"tags": {"type": "array", "items": {"type": "string"}},
				"extra": {"x-kubernetes-preserve-unknown-fields": true}
			}
		},
		"status": {"type": "object", "properties": {"ready": {"type": "boolean"}}}
	}
}`

func newCRD() *api.CustomResourceDefinition {
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: "widgets.apps.gaon.garam.sh"},
		Spec: api.CustomResourceDefinitionSpec{
			Group: "apps.gaon.garam.sh",
			Names: api.CustomResourceNames{Kind: "Widget", ListKind: "WidgetList", Plural: "widgets", Singular: "widget"},
			Scope: api.ScopeCluster,
			Versions: []api.CustomResourceVersion{{
				Name: "v1alpha1", Served: true, Storage: true,
				Schema: api.CustomResourceValidation{OpenAPIV3Schema: decodeSchemaFor(widgetSchema)},
			}},
		},
	}
}

func decodeSchemaFor(doc string) *api.JSONSchemaProps {
	var s api.JSONSchemaProps
	if err := json.Unmarshal([]byte(doc), &s); err != nil {
		panic(err)
	}
	return &s
}

func TestCustomResourceDefinitionValidateRefusesAName(t *testing.T) {
	require.NoError(t, newCRD().Validate(), "control: <plural>.<group> is accepted")

	d := newCRD()
	d.Metadata.Name = "widget.apps.gaon.garam.sh"
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "the singular is not the plural")
	d = newCRD()
	d.Metadata.Name = "widgets"
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid)
	d = newCRD()
	d.Metadata.Name = ""
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid)
}

func TestCustomResourceDefinitionValidateChecksIdentity(t *testing.T) {
	d := newCRD()
	d.APIVersion = api.GroupVersion
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "a definition is in the apiextensions group")
	d = newCRD()
	d.Kind = api.KindClusterRole
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid)
}

func TestCustomResourceDefinitionValidateRefusesAReservedGroup(t *testing.T) {
	for _, group := range []string{"apps.gaon.garam.sh", "example.com", "x.y.z"} {
		d := newCRD()
		d.Spec.Group = group
		d.Metadata.Name = "widgets." + group
		assert.NoError(t, d.Validate(), "control: %s is not served by the core", group)
	}
	for _, group := range []string{api.Group, api.RBACGroup, api.APIExtensionsGroup} {
		d := newCRD()
		d.Spec.Group = group
		d.Metadata.Name = "widgets." + group
		assert.ErrorIs(t, d.Validate(), api.ErrInvalid, group)
	}
}

func TestCustomResourceDefinitionValidateRefusesAGroupThatIsNotADomain(t *testing.T) {
	for _, group := range []string{"", "apps", "Apps.Example.com", "apps..example.com", "-a.example.com", "a_b.example.com"} {
		d := newCRD()
		d.Spec.Group = group
		d.Metadata.Name = "widgets." + group
		assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "group %q", group)
	}
}

func TestCustomResourceDefinitionValidateRefusesAScopeButCluster(t *testing.T) {
	require.NoError(t, newCRD().Validate(), "control: Cluster is accepted")
	for _, scope := range []api.ResourceScope{"Namespaced", "", "cluster"} {
		d := newCRD()
		d.Spec.Scope = scope
		assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "scope %q", scope)
	}
}

func TestCustomResourceDefinitionValidateRefusesBadNames(t *testing.T) {
	require.NoError(t, newCRD().Validate(), "control: well-formed names are accepted")
	for name, edit := range map[string]func(*api.CustomResourceNames){
		"an empty singular":     func(n *api.CustomResourceNames) { n.Singular = "" },
		"an uppercase singular": func(n *api.CustomResourceNames) { n.Singular = "Widget" },
		"a lowercase kind":      func(n *api.CustomResourceNames) { n.Kind = "widget" },
		"an empty listKind":     func(n *api.CustomResourceNames) { n.ListKind = "" },
		"a kind with a dash":    func(n *api.CustomResourceNames) { n.Kind = "Wid-get" },
		"listKind as kind":      func(n *api.CustomResourceNames) { n.ListKind = n.Kind },
	} {
		d := newCRD()
		edit(&d.Spec.Names)
		assert.ErrorIs(t, d.Validate(), api.ErrInvalid, name)
	}
	d := newCRD()
	d.Spec.Names.Plural = "Widgets"
	d.Metadata.Name = "Widgets.apps.gaon.garam.sh"
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "an uppercase plural, with a name that matches it")
}

func TestCustomResourceDefinitionValidateRefusesAnythingButOneServedStoredVersion(t *testing.T) {
	require.NoError(t, newCRD().Validate(), "control: one version, served and stored")

	d := newCRD()
	d.Spec.Versions = nil
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "no version")
	d = newCRD()
	second := d.Spec.Versions[0]
	second.Name, second.Storage = "v1beta1", false
	d.Spec.Versions = append(d.Spec.Versions, second)
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "two versions need conversion")
	d = newCRD()
	d.Spec.Versions[0].Served = false
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "not served")
	d = newCRD()
	d.Spec.Versions[0].Storage = false
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "not stored")
	for _, name := range []string{"", "1", "alpha1", "v0", "v1alpha", "V1", "v1.1"} {
		d = newCRD()
		d.Spec.Versions[0].Name = name
		assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "version name %q", name)
	}
	for _, name := range []string{"v1", "v2beta3", "v1alpha1"} {
		d = newCRD()
		d.Spec.Versions[0].Name = name
		assert.NoError(t, d.Validate(), "control: version name %q", name)
	}
	d = newCRD()
	d.Spec.Versions[0].Schema.OpenAPIV3Schema = nil
	assert.ErrorIs(t, d.Validate(), api.ErrInvalid, "no schema")
}

func TestCustomResourceDefinitionValidateAcceptsTheStatusSubresource(t *testing.T) {
	d := newCRD()
	d.Spec.Versions[0].Subresources = &api.CustomResourceSubresources{Status: &api.StatusSubresource{}}
	require.NoError(t, d.Validate())

	raw, err := json.Marshal(d)
	require.NoError(t, err)
	var back api.CustomResourceDefinition
	require.NoError(t, json.Unmarshal(raw, &back))
	require.NotNil(t, back.Spec.Versions[0].Subresources)
	assert.NotNil(t, back.Spec.Versions[0].Subresources.Status, "the status subresource survives its JSON {}")
	assert.Contains(t, string(raw), `"status":{}`)
	assert.Contains(t, string(raw), `"x-kubernetes-preserve-unknown-fields":true`)
}

func TestCustomResourceDefinitionValidateUpdateKeepsWhatObjectsDependOn(t *testing.T) {
	old := newCRD()
	edited := newCRD()
	edited.Spec.Versions[0].Subresources = &api.CustomResourceSubresources{Status: &api.StatusSubresource{}}
	edited.Spec.Versions[0].Schema.OpenAPIV3Schema = decodeSchemaFor(`{"type":"object","properties":{"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}}`)
	assert.NoError(t, edited.ValidateUpdate(old), "control: the schema and the subresources may change")

	for name, edit := range map[string]func(*api.CustomResourceDefinition){
		"group":        func(d *api.CustomResourceDefinition) { d.Spec.Group = "other.example.com" },
		"kind":         func(d *api.CustomResourceDefinition) { d.Spec.Names.Kind = "Gizmo" },
		"listKind":     func(d *api.CustomResourceDefinition) { d.Spec.Names.ListKind = "GizmoList" },
		"singular":     func(d *api.CustomResourceDefinition) { d.Spec.Names.Singular = "gizmo" },
		"plural":       func(d *api.CustomResourceDefinition) { d.Spec.Names.Plural = "gizmos" },
		"version name": func(d *api.CustomResourceDefinition) { d.Spec.Versions[0].Name = "v1" },
	} {
		d := newCRD()
		edit(d)
		assert.ErrorIs(t, d.ValidateUpdate(old), api.ErrInvalid, name)
	}
}

func TestCustomResourceDefinitionValidateStatusRefusesAnUnknownCondition(t *testing.T) {
	d := newCRD()
	d.Status.Conditions = []api.Condition{
		{Type: api.CRDNamesAccepted, Status: api.ConditionTrue},
		{Type: api.CRDEstablished, Status: api.ConditionFalse},
	}
	require.NoError(t, d.ValidateStatus(), "control: both types, with known statuses")

	d.Status.Conditions = []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}
	assert.ErrorIs(t, d.ValidateStatus(), api.ErrInvalid, "an unknown type")
	d.Status.Conditions = []api.Condition{{Type: api.CRDEstablished, Status: "Maybe"}}
	assert.ErrorIs(t, d.ValidateStatus(), api.ErrInvalid, "an unknown status")
}

func TestClientCustomResourceDefinitionPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetCustomResourceDefinition(ctx, "widgets.apps.gaon.garam.sh")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutCustomResourceDefinition(ctx, newCRD())
	require.NoError(t, err)
	_, err = c.PutCustomResourceDefinition(ctx, newCRD())
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	bad := newCRD()
	bad.Spec.Scope = "Namespaced"
	bad.Metadata.Name = "gadgets.apps.gaon.garam.sh"
	_, err = c.PutCustomResourceDefinition(ctx, bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "the server validates what the client sends")

	got, err := c.GetCustomResourceDefinition(ctx, "widgets.apps.gaon.garam.sh")
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Versions[0].Schema.OpenAPIV3Schema)
	assert.Equal(t, "Widget", got.Spec.Names.Kind)
	assert.Len(t, got.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties, 5, "the schema crosses the wire whole")

	list, err := c.ListCustomResourceDefinitions(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindCustomResourceDefinitionList, list.Kind)
	assert.Len(t, list.Items, 1)

	_, err = c.DeleteCustomResourceDefinition(ctx, "widgets.apps.gaon.garam.sh", "")
	assert.ErrorContains(t, err, "resource version is empty", "a delete names the version it deletes, before any request")
	_, err = c.DeleteCustomResourceDefinition(ctx, "widgets.apps.gaon.garam.sh", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetCustomResourceDefinition(ctx, "widgets.apps.gaon.garam.sh")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClientWatchCustomResourceDefinitionsDeliversDefinitionEventsOnly(t *testing.T) {
	ctx := context.Background()
	_, c := newMemoryServer(t)
	_, err := c.PutClusterRole(ctx, newClusterRole("reader"))
	require.NoError(t, err)
	_, err = c.PutCustomResourceDefinition(ctx, newCRD())
	require.NoError(t, err)

	w, err := c.WatchCustomResourceDefinitions(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "widgets.apps.gaon.garam.sh", ev.Object.Metadata.Name, "the role's event is not delivered to a definition watch")

	_, err = c.WatchCustomResourceDefinitions(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}

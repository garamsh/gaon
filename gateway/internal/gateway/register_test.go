package gateway_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

var definitionNames = []string{"gateways.gateway.gaon.garam.sh", "httproutes.gateway.gaon.garam.sh", "tlsroutes.gateway.gaon.garam.sh"}

func TestRun_RegistersTheThreeDefinitions(t *testing.T) {
	f := newFakeAPI(t)
	f.start(t)

	for _, name := range definitionNames {
		eventually(t, func() bool { _, ok := f.definition(name); return ok }, name+" is registered")
	}
	kinds := map[string][2]string{
		definitionNames[0]: {"Gateway", "gateways"},
		definitionNames[1]: {"HTTPRoute", "httproutes"},
		definitionNames[2]: {"TLSRoute", "tlsroutes"},
	}
	for name, want := range kinds {
		d, _ := f.definition(name)
		assert.Equal(t, gateway.Group, d.Spec.Group, name)
		assert.Equal(t, api.ScopeCluster, d.Spec.Scope, name)
		assert.Equal(t, want[0], d.Spec.Names.Kind, name)
		assert.Equal(t, want[1], d.Spec.Names.Plural, name)
		assert.Equal(t, want[0]+"List", d.Spec.Names.ListKind, name)
		assert.Equal(t, gateway.Manager, d.Metadata.Labels[gateway.ManagedByLabel], name)
		require.Len(t, d.Spec.Versions, 1, name)
		v := d.Spec.Versions[0]
		assert.Equal(t, "v1alpha1", v.Name, name)
		assert.True(t, v.Served && v.Storage, name)
		require.NotNil(t, v.Subresources, name)
		assert.NotNil(t, v.Subresources.Status, name)
		require.NotNil(t, v.Schema.OpenAPIV3Schema, name)
		assert.Contains(t, v.Schema.OpenAPIV3Schema.Properties, "spec", name)
		assert.Contains(t, v.Schema.OpenAPIV3Schema.Properties, "status", name)
	}
	gw, _ := f.definition(definitionNames[0])
	listeners := gw.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["listeners"]
	require.NotNil(t, listeners.Items)
	assert.ElementsMatch(t, []string{"name", "port", "protocol"}, listeners.Items.Required)
}

func TestRun_LeavesAMatchingDefinitionAloneAndUpdatesOneThatDiffers(t *testing.T) {
	f := newFakeAPI(t)
	f.start(t)
	for _, name := range definitionNames {
		eventually(t, func() bool { _, ok := f.definition(name); return ok }, name+" is registered")
	}
	f.settle(t)
	for _, name := range definitionNames {
		assert.Equal(t, 1, f.putsOf("customresourcedefinitions/"+name), "control: a stored definition that matches is not written again")
	}

	// A later start finds the definition of an older version and brings it forward.
	f.mu.Lock()
	stored := f.kind("customresourcedefinitions")[definitionNames[1]]
	spec := stored["spec"].(map[string]any)
	spec["names"].(map[string]any)["singular"] = "older"
	f.mu.Unlock()
	f.start(t)
	eventually(t, func() bool {
		d, _ := f.definition(definitionNames[1])
		return d.Spec.Names.Singular == "httproute"
	}, "the second service rewrites the definition that differs")
	assert.Equal(t, 1, f.putsOf("customresourcedefinitions/"+definitionNames[0]), "the others stay as they were")
}

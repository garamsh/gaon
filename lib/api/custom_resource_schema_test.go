package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// crdWithSchema returns a valid definition whose schema is doc.
func crdWithSchema(doc string) *api.CustomResourceDefinition {
	d := newCRD()
	d.Spec.Versions[0].Schema.OpenAPIV3Schema = decodeSchemaFor(doc)
	return d
}

func TestSchemaAcceptsTheSupportedSubset(t *testing.T) {
	require.NoError(t, newCRD().Validate(), "the widget schema uses every supported keyword")
}

func TestSchemaRootNamesOnlySpecAndStatus(t *testing.T) {
	require.NoError(t, crdWithSchema(`{"type":"object","properties":{"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}}`).Validate(), "control")
	assert.ErrorIs(t, crdWithSchema(`{"type":"object","properties":{"data":{"type":"string"}}}`).Validate(), api.ErrInvalid, "a root property besides spec and status")
	assert.ErrorIs(t, crdWithSchema(`{"type":"string"}`).Validate(), api.ErrInvalid, "a root that is not an object")
	assert.ErrorIs(t, crdWithSchema(`{"properties":{"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}}`).Validate(), api.ErrInvalid, "a root with no type")
	assert.ErrorIs(t, crdWithSchema(`{"type":"object","x-kubernetes-preserve-unknown-fields":true}`).Validate(), api.ErrInvalid, "a root that keeps unknown fields")
}

func TestSchemaRefusesAKeywordOutsideTheSubset(t *testing.T) {
	require.NoError(t, crdWithSchema(`{"type":"object","properties":{"spec":{"type":"object","properties":{"a":{"type":"string"}}}}}`).Validate(), "control")
	assert.ErrorIs(t, crdWithSchema(`{"type":"object","oneOf":[],"properties":{"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true}}}`).Validate(), api.ErrInvalid, "at the root")
	assert.ErrorIs(t, crdWithSchema(`{"type":"object","properties":{"spec":{"type":"object","properties":{"a":{"type":"string","format":"date"}}}}}`).Validate(), api.ErrInvalid, "nested in properties")
	assert.ErrorIs(t, crdWithSchema(`{"type":"object","properties":{"spec":{"type":"object","properties":{"a":{"type":"array","items":{"type":"string","default":"x"}}}}}}`).Validate(), api.ErrInvalid, "nested in items")
	assert.ErrorIs(t, crdWithSchema(`{"type":"object","properties":{"spec":{"type":"object","additionalProperties":true}}}`).Validate(), api.ErrInvalid, "additionalProperties")
}

func TestSchemaNodeNeedsAKnownType(t *testing.T) {
	spec := func(node string) string {
		return `{"type":"object","properties":{"spec":{"type":"object","properties":{"a":` + node + `}}}}`
	}
	require.NoError(t, crdWithSchema(spec(`{"type":"string"}`)).Validate(), "control: a typed node")
	require.NoError(t, crdWithSchema(spec(`{"x-kubernetes-preserve-unknown-fields":true}`)).Validate(), "control: preserve stands in for a type")
	assert.ErrorIs(t, crdWithSchema(spec(`{}`)).Validate(), api.ErrInvalid, "no type")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"null"}`)).Validate(), api.ErrInvalid, "an unknown type")
	assert.ErrorIs(t, crdWithSchema(spec(`{"description":"only words"}`)).Validate(), api.ErrInvalid, "a description is not a type")
}

func TestSchemaArrayNeedsItemsAndOnlyArraysHaveThem(t *testing.T) {
	spec := func(node string) string {
		return `{"type":"object","properties":{"spec":{"type":"object","properties":{"a":` + node + `}}}}`
	}
	require.NoError(t, crdWithSchema(spec(`{"type":"array","items":{"type":"string"}}`)).Validate(), "control")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"array"}`)).Validate(), api.ErrInvalid, "an array with no items")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"string","items":{"type":"string"}}`)).Validate(), api.ErrInvalid, "items on a string")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"array","items":{"type":"array"}}`)).Validate(), api.ErrInvalid, "the items are checked too")
}

func TestSchemaPropertiesAndRequiredBelongToObjects(t *testing.T) {
	spec := func(node string) string {
		return `{"type":"object","properties":{"spec":{"type":"object","properties":{"a":` + node + `}}}}`
	}
	require.NoError(t, crdWithSchema(spec(`{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}`)).Validate(), "control")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"string","properties":{"b":{"type":"string"}}}`)).Validate(), api.ErrInvalid, "properties on a string")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"object","properties":{"b":{"type":"string"}},"required":["c"]}`)).Validate(), api.ErrInvalid, "a required name no property names")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"object","properties":{"":{"type":"string"}}}`)).Validate(), api.ErrInvalid, "an empty property name")
	assert.NoError(t, crdWithSchema(spec(`{"type":"object","required":["c"],"x-kubernetes-preserve-unknown-fields":true}`)).Validate(), "required may name a preserved field")
}

func TestSchemaEnumTakesTypedScalars(t *testing.T) {
	spec := func(node string) string {
		return `{"type":"object","properties":{"spec":{"type":"object","properties":{"a":` + node + `}}}}`
	}
	require.NoError(t, crdWithSchema(spec(`{"type":"string","enum":["a","b"]}`)).Validate(), "control: strings")
	require.NoError(t, crdWithSchema(spec(`{"type":"integer","enum":[1,2]}`)).Validate(), "control: integers")
	require.NoError(t, crdWithSchema(spec(`{"type":"boolean","enum":[true]}`)).Validate(), "control: booleans")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"string","enum":["a",1]}`)).Validate(), api.ErrInvalid, "a number in a string enum")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"integer","enum":[1.5]}`)).Validate(), api.ErrInvalid, "a fraction in an integer enum")
	err := crdWithSchema(spec(`{"type":"object","enum":[{}]}`)).Validate()
	assert.ErrorIs(t, err, api.ErrInvalid, "an enum on an object")
	assert.ErrorContains(t, err, "only a scalar type", "the refusal names the type, not a value")
}

func TestSchemaBoundsBelongToNumbers(t *testing.T) {
	spec := func(node string) string {
		return `{"type":"object","properties":{"spec":{"type":"object","properties":{"a":` + node + `}}}}`
	}
	require.NoError(t, crdWithSchema(spec(`{"type":"number","minimum":1,"maximum":1}`)).Validate(), "control: equal bounds")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"string","minimum":1}`)).Validate(), api.ErrInvalid, "minimum on a string")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"string","maximum":1}`)).Validate(), api.ErrInvalid, "maximum on a string")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"integer","minimum":5,"maximum":1}`)).Validate(), api.ErrInvalid, "minimum above maximum")
}

func TestSchemaPatternBelongsToStringsAndCompiles(t *testing.T) {
	spec := func(node string) string {
		return `{"type":"object","properties":{"spec":{"type":"object","properties":{"a":` + node + `}}}}`
	}
	require.NoError(t, crdWithSchema(spec(`{"type":"string","pattern":"^[a-z]+$"}`)).Validate(), "control")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"integer","pattern":"^1$"}`)).Validate(), api.ErrInvalid, "pattern on an integer")
	assert.ErrorIs(t, crdWithSchema(spec(`{"type":"string","pattern":"("}`)).Validate(), api.ErrInvalid, "a pattern that does not compile")
}

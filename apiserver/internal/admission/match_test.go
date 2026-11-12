package admission

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchesResource(t *testing.T) {
	for _, c := range []struct {
		pattern, resource, sub string
		want                   bool
	}{
		{"microvms", "microvms", "", true},
		{"microvms", "nodes", "", false},
		{"microvms", "microvms", "status", false},
		{"microvms/status", "microvms", "status", true},
		{"microvms/status", "microvms", "", false},
		{"microvms/status", "microvms", "binding", false},
		{"microvms/*", "microvms", "binding", true},
		{"microvms/*", "microvms", "", true},
		{"microvms/*", "nodes", "status", false},
		{"*/status", "nodes", "status", true},
		{"*/status", "nodes", "", false},
		{"*", "nodes", "", true},
		{"*", "nodes", "status", false},
		{"*/*", "nodes", "status", true},
		{"*/*", "nodes", "", true},
	} {
		assert.Equal(t, c.want, matchesResource(c.pattern, c.resource, c.sub), "%s against %s/%s", c.pattern, c.resource, c.sub)
	}
}

func TestApplyPatch_RefusesAChangeToAnIdentityField(t *testing.T) {
	obj := []byte(`{"apiVersion":"v1","kind":"K","metadata":{"name":"n","resourceVersion":"3"},"spec":{"a":1}}`)

	patched, err := applyPatch(obj, []byte(`[{"op":"replace","path":"/spec/a","value":2}]`), nil)
	assert.NoError(t, err)
	assert.JSONEq(t, `{"apiVersion":"v1","kind":"K","metadata":{"name":"n","resourceVersion":"3"},"spec":{"a":2}}`, string(patched))

	for _, path := range []string{"/apiVersion", "/kind", "/metadata/name", "/metadata/resourceVersion"} {
		_, err := applyPatch(obj, []byte(`[{"op":"replace","path":"`+path+`","value":"x"}]`), nil)
		assert.Error(t, err, path)
	}
}

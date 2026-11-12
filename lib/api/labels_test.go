package api_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestValidateLabelsAcceptsKubernetesSyntax(t *testing.T) {
	require.NoError(t, api.ValidateLabels(map[string]string{
		"app":                   "web",
		"example.com/tier":      "front_end-1.0",
		"a":                     "",
		"k8s.io/name":           "A",
		strings.Repeat("a", 63): strings.Repeat("b", 63),
		strings.Repeat("a", 63) + "/" + strings.Repeat("c", 63): "x",
	}))
}

func TestValidateLabelsRefusesABadKey(t *testing.T) {
	require.NoError(t, api.ValidateLabels(map[string]string{"app": "web"}), "control")

	for _, key := range []string{
		"",
		"-app",
		"app-",
		"ap p",
		"a/b/c",
		"/name",
		"Example.com/name",
		"example..com/name",
		"example.com/",
		strings.Repeat("a", 64),
		strings.Repeat("a", 254) + "/name",
	} {
		assert.ErrorIs(t, api.ValidateLabels(map[string]string{key: "v"}), api.ErrInvalid, "key %q", key)
	}
}

func TestValidateLabelsRefusesABadValue(t *testing.T) {
	require.NoError(t, api.ValidateLabels(map[string]string{"app": "web"}), "control")

	for _, value := range []string{"-v", "v-", "a b", "a/b", strings.Repeat("v", 64)} {
		assert.ErrorIs(t, api.ValidateLabels(map[string]string{"app": value}), api.ErrInvalid, "value %q", value)
	}
}

func TestValidateAnnotationsAcceptsAnyValueUpToTheTotalSize(t *testing.T) {
	require.NoError(t, api.ValidateAnnotations(map[string]string{"note": "any text, with spaces/and: punctuation"}))
	// key "k" plus a value that brings the total to exactly 256 KiB
	require.NoError(t, api.ValidateAnnotations(map[string]string{"k": strings.Repeat("v", 256*1024-1)}))
}

func TestValidateAnnotationsRefusesABadKeyAndAnOversizeTotal(t *testing.T) {
	assert.ErrorIs(t, api.ValidateAnnotations(map[string]string{"bad key": "v"}), api.ErrInvalid)
	assert.ErrorIs(t, api.ValidateAnnotations(map[string]string{"k": strings.Repeat("v", 256*1024)}), api.ErrInvalid)

	half := strings.Repeat("v", 128*1024)
	assert.ErrorIs(t, api.ValidateAnnotations(map[string]string{"a": half, "b": half}), api.ErrInvalid,
		"the size is the total of the annotations, not of each")
}

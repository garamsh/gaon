package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/lib/api"
)

// "1A" breaks only the first-character rule, and "A1" is the nearest name that
// does not.
func TestValidEnvName(t *testing.T) {
	tests := map[string]bool{
		"1A":      false,
		"A1":      true,
		"-a":      true,
		".a":      true,
		"_a":      true,
		"a.b-c_1": true,
		"A B":     false,
		"A=B":     false,
		"":        false,
		"a\n":     false,
	}
	for name, want := range tests {
		assert.Equal(t, want, api.ValidEnvName(name), "%q", name)
	}
}

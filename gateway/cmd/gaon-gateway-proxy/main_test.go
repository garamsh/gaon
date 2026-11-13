package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
)

func TestParseFlags(t *testing.T) {
	required := []string{"-gateway", "edge", "-server", "https://api.test", "-tls-ca", "/ca.crt"}
	without := func(flag string) []string {
		var out []string
		for i := 0; i < len(required); i += 2 {
			if required[i] != flag {
				out = append(out, required[i], required[i+1])
			}
		}
		return out
	}
	t.Run("the required flags are enough, and the token file has its default", func(t *testing.T) {
		cfg, err := parseFlags(required, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, config{gateway: "edge", server: "https://api.test", caFile: "/ca.crt", tokenFile: tokenPath}, cfg)
	})
	t.Run("the token file can be named", func(t *testing.T) {
		cfg, err := parseFlags(append([]string{"-token-file", "/token"}, required...), io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "/token", cfg.tokenFile)
	})
	for _, flag := range []string{"-gateway", "-server", "-tls-ca"} {
		t.Run("a missing "+flag+" is refused", func(t *testing.T) {
			_, err := parseFlags(without(flag), io.Discard)
			assert.Error(t, err)
		})
	}
}

// TestParseFlags_TakesTheArgumentsTheControllerStartsItWith keeps the flags of
// the proxy and the arguments of the controller's Deployment in step.
func TestParseFlags_TakesTheArgumentsTheControllerStartsItWith(t *testing.T) {
	cfg, err := parseFlags(gateway.ProxyArgs("edge", "https://api.test"), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "edge", cfg.gateway)
	assert.Equal(t, "https://api.test", cfg.server)
	assert.Equal(t, gateway.ProxyCAPath, cfg.caFile)
}

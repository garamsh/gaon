package networkdefault_test

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/plugin"
	networkdefault "github.com/garamsh/gaon/network-default"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseConfig_DefaultsToTheDefaultPluginDirectoryAndNoPrefixes(t *testing.T) {
	c, err := networkdefault.ParseConfig(nil, env(nil), io.Discard)

	require.NoError(t, err)
	assert.Equal(t, plugin.DefaultDir, c.PluginDir)
	assert.Empty(t, c.Egress)
	assert.Empty(t, c.Ingress)
}

func TestParseConfig_ReadsFlagsAndEnvironmentAndAFlagReplacesItsVariable(t *testing.T) {
	c, err := networkdefault.ParseConfig(nil, env(map[string]string{
		networkdefault.EnvPluginDir:     "/env/dir",
		networkdefault.EnvEgressPrefix:  "10.0.0.0/8,fd00::/8",
		networkdefault.EnvIngressPrefix: "192.168.0.0/16",
	}), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "/env/dir", c.PluginDir)
	assert.Equal(t, []string{"10.0.0.0/8", "fd00::/8"}, c.Egress)
	assert.Equal(t, []string{"192.168.0.0/16"}, c.Ingress)

	c, err = networkdefault.ParseConfig(
		[]string{"--plugin-dir=/flag/dir", "--egress-prefix=172.16.0.0/12", "--egress-prefix=fd01::/16"},
		env(map[string]string{networkdefault.EnvPluginDir: "/env/dir", networkdefault.EnvEgressPrefix: "bad", networkdefault.EnvIngressPrefix: "192.168.0.0/16"}),
		io.Discard)
	require.NoError(t, err, "a flag replaces its variable, which is not parsed then")
	assert.Equal(t, "/flag/dir", c.PluginDir)
	assert.Equal(t, []string{"172.16.0.0/12", "fd01::/16"}, c.Egress)
	assert.Equal(t, []string{"192.168.0.0/16"}, c.Ingress, "control: a variable whose flag is not given still applies")
}

func TestParseConfig_StopsOnAPrefixThatIsNotACIDRAndOnAStrayArgument(t *testing.T) {
	for _, args := range [][]string{
		{"--egress-prefix=10.0.0.0/8", "--egress-prefix=nonsense"},
		{"--ingress-prefix=300.0.0.0/8"},
		{"stray"},
	} {
		_, err := networkdefault.ParseConfig(args, env(nil), io.Discard)
		assert.Error(t, err, "%v", args)
	}
	_, err := networkdefault.ParseConfig(nil, env(map[string]string{networkdefault.EnvIngressPrefix: "10.0.0.0/8,10.1.2.3"}), io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.1.2.3")
}

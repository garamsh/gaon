package nodeagent_test

import (
	"io"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nodeagent "github.com/garamsh/gaon/node-agent"
)

func TestParseConfig_ReadsWhereTheAgentServesFromFlagsAndEnvironmentAndNoneWhenAbsent(t *testing.T) {
	a := newMTLSAPI(t)
	base := append([]string{"--api-server", a.URL}, a.flags()...)

	none, err := nodeagent.ParseConfig(base, env(required), io.Discard)
	require.NoError(t, err)
	assert.Empty(t, none.AgentAddresses, "control: neither flag, nothing served")
	assert.Zero(t, none.AgentPort)

	cfg, err := nodeagent.ParseConfig(append(base, "--agent-addresses", "10.0.0.7, fd00::7", "--agent-port", "10250"), env(required), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("10.0.0.7"), netip.MustParseAddr("fd00::7")}, cfg.AgentAddresses)
	assert.Equal(t, 10250, cfg.AgentPort)

	environment := map[string]string{nodeagent.EnvAgentAddresses: "10.0.0.8", nodeagent.EnvAgentPort: "10251"}
	for k, v := range required {
		environment[k] = v
	}
	cfg, err = nodeagent.ParseConfig(base, env(environment), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, 10251, cfg.AgentPort, "the environment gives the port when no flag does")

	cfg, err = nodeagent.ParseConfig(append(base, "--agent-port", "10252"), env(environment), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("10.0.0.8")}, cfg.AgentAddresses, "the environment fills a flag not given")
	assert.Equal(t, 10252, cfg.AgentPort, "a flag wins over the environment")
}

func TestParseConfig_RefusesWhereTheAgentServesWhenItCannotServeThere(t *testing.T) {
	a := newMTLSAPI(t)
	mtls := append([]string{"--api-server", a.URL}, a.flags()...)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"addresses without a port", []string{"--agent-addresses", "10.0.0.7"}, "together"},
		{"a port without addresses", []string{"--agent-port", "10250"}, "together"},
		{"a port above 65535", []string{"--agent-addresses", "10.0.0.7", "--agent-port", "70000"}, "65535"},
		{"a name for an address", []string{"--agent-addresses", "node-a.example", "--agent-port", "10250"}, "agent-addresses"},
		{"the unspecified address", []string{"--agent-addresses", "0.0.0.0", "--agent-port", "10250"}, "not an address of the Node"},
		{"an address with a zone", []string{"--agent-addresses", "fe80::1%eth0", "--agent-port", "10250"}, "not an address of the Node"},
		{"an address twice", []string{"--agent-addresses", "10.0.0.7,10.0.0.7", "--agent-port", "10250"}, "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := nodeagent.ParseConfig(append(append([]string{}, mtls...), tt.args...), env(required), io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	_, err := nodeagent.ParseConfig(append(append([]string{}, mtls...), "--agent-addresses", "10.0.0.7", "--agent-port", "10250"), env(required), io.Discard)
	assert.NoError(t, err, "control: a valid pair with the TLS files is accepted")

	serve := []string{"--agent-addresses", "10.0.0.7", "--agent-port", "10250"}
	certDir := append([]string{"--api-server", a.URL, "--tls-ca", a.ca, "--cert-dir", t.TempDir()}, serve...)
	_, err = nodeagent.ParseConfig(certDir, env(required), io.Discard)
	assert.NoError(t, err, "control: the agent's own certificate in --cert-dir is one to serve with")

	noCertificate := []struct {
		name string
		args []string
	}{
		{"plain HTTP to the API server", append([]string{"--api-server", a.URL}, serve...)},
		{"a CA and no certificate", append([]string{"--api-server", a.URL, "--tls-ca", a.ca}, serve...)},
		{"a certificate and no CA", append([]string{"--api-server", a.URL, "--tls-cert", a.cert, "--tls-key", a.key}, serve...)},
	}
	for _, tt := range noCertificate {
		t.Run(tt.name, func(t *testing.T) {
			_, err := nodeagent.ParseConfig(tt.args, env(required), io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--agent-addresses needs --tls-ca and a certificate to serve with")
		})
	}
}

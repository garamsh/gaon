package networkdefault_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	networkdefault "github.com/garamsh/gaon/network-default"
)

func microvm(ports []int32, networks ...string) *api.MicroVM {
	m := &api.MicroVM{}
	m.Metadata.Name = "web"
	for _, p := range ports {
		m.Spec.Ports = append(m.Spec.Ports, api.MicroVMPort{Port: p, Protocol: api.ProtocolTCP})
	}
	m.Spec.Networks = networks
	return m
}

func policy(t *testing.T, egress, ingress []string, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	t.Helper()
	d, err := networkdefault.NewDefault(egress, ingress)
	require.NoError(t, err)
	return d.Policy(t.Context(), m)
}

func TestPolicy_IsOneAnyRulePerEgressPrefixAndOneTCPRulePerPortAndIngressPrefix(t *testing.T) {
	got, err := policy(t, []string{"10.0.0.0/8", "2001:db8::/32"}, []string{"192.168.0.0/16"}, microvm([]int32{80, 443}))

	require.NoError(t, err)
	tcp := func(port uint32) plugin.NetworkRule {
		return plugin.NetworkRule{Prefix: "192.168.0.0/16", Protocol: plugin.TCP, Ports: []plugin.PortRange{{First: port, Last: port}}}
	}
	assert.Equal(t, &plugin.NetworkPolicy{
		Egress: []plugin.NetworkRule{
			{Prefix: "10.0.0.0/8", Protocol: plugin.Any},
			{Prefix: "2001:db8::/32", Protocol: plugin.Any},
		},
		Ingress: []plugin.NetworkRule{tcp(80), tcp(443)},
	}, got)

	quiet, err := policy(t, []string{"10.0.0.0/8", "2001:db8::/32"}, []string{"192.168.0.0/16"}, microvm(nil))
	require.NoError(t, err)
	assert.Len(t, quiet.Egress, 2, "control: egress does not depend on the ports")
	assert.Empty(t, quiet.Ingress, "a MicroVM with no ports accepts nothing")
}

func TestPolicy_WithNoPrefixesIsEmptyWhateverThePorts(t *testing.T) {
	got, err := policy(t, nil, nil, microvm([]int32{80}))

	require.NoError(t, err)
	assert.Empty(t, got.Egress)
	assert.Empty(t, got.Ingress, "a port opens nothing until an ingress prefix is given")
}

func TestPolicy_RefusesANetworkItDoesNotServeAndServesDefaultOrNone(t *testing.T) {
	_, err := policy(t, nil, nil, microvm(nil, "default", "tenant-b"))
	var r *plugin.Refusal
	require.True(t, errors.As(err, &r))
	assert.Equal(t, networkdefault.ReasonUnknownNetwork, r.Reason)
	assert.Contains(t, r.Message, "tenant-b")

	for _, networks := range [][]string{nil, {"default"}} {
		_, err := policy(t, nil, nil, microvm(nil, networks...))
		assert.NoError(t, err, "control: networks %v are served", networks)
	}
}

func TestPolicy_RefusesAPortMvmdWouldRefuseAndServesTheLimits(t *testing.T) {
	for _, port := range []int32{0, -1, 70000, 65536} {
		_, err := policy(t, nil, []string{"10.0.0.0/8"}, microvm([]int32{port}))
		var r *plugin.Refusal
		require.True(t, errors.As(err, &r), "port %d", port)
		assert.Equal(t, networkdefault.ReasonInvalidSpec, r.Reason)
	}
	for _, port := range []int32{1, 65535} {
		_, err := policy(t, nil, []string{"10.0.0.0/8"}, microvm([]int32{port}))
		assert.NoError(t, err, "control: port %d is served", port)
	}
}

func TestNewDefault_RefusesAPrefixThatIsNotACIDR(t *testing.T) {
	_, err := networkdefault.NewDefault([]string{"10.0.0.0/8", "10.1.2.3"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.1.2.3")
	_, err = networkdefault.NewDefault(nil, []string{"300.0.0.0/8"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "300.0.0.0/8")
}

func TestPolicy_AllowsAPortWithTheProtocolItDeclares(t *testing.T) {
	m := microvm(nil)
	m.Spec.Ports = []api.MicroVMPort{
		{Port: 53, Protocol: api.ProtocolUDP},
		{Port: 53, Protocol: api.ProtocolTCP},
		{Port: 5000, Protocol: api.ProtocolUDP},
	}

	got, err := policy(t, nil, []string{"192.168.0.0/16"}, m)

	require.NoError(t, err)
	rule := func(protocol plugin.Protocol, port uint32) plugin.NetworkRule {
		return plugin.NetworkRule{Prefix: "192.168.0.0/16", Protocol: protocol, Ports: []plugin.PortRange{{First: port, Last: port}}}
	}
	assert.Equal(t, []plugin.NetworkRule{
		rule(plugin.UDP, 53),
		rule(plugin.TCP, 53),
		rule(plugin.UDP, 5000),
	}, got.Ingress, "UDP is allowed as UDP and TCP as TCP, each the other's control")
}

func TestPolicy_RefusesAPortWhoseProtocolIsNeitherTCPNorUDP(t *testing.T) {
	for _, protocol := range []api.Protocol{"", "SCTP", "tcp"} {
		m := microvm(nil)
		m.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: protocol}}
		_, err := policy(t, nil, []string{"10.0.0.0/8"}, m)
		var r *plugin.Refusal
		require.True(t, errors.As(err, &r), "protocol %q", protocol)
		assert.Equal(t, networkdefault.ReasonInvalidSpec, r.Reason)
	}
	for _, protocol := range []api.Protocol{api.ProtocolTCP, api.ProtocolUDP} {
		m := microvm(nil)
		m.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: protocol}}
		_, err := policy(t, nil, []string{"10.0.0.0/8"}, m)
		assert.NoError(t, err, "control: protocol %q is served", protocol)
	}
}

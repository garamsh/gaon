package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newNetworkPolicy(name string) *api.NetworkPolicy {
	web := api.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
	return &api.NetworkPolicy{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNetworkPolicy},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.NetworkPolicySpec{
			PodSelector: web,
			Ingress: []api.IngressRule{{
				From:  []api.NetworkPolicyPeer{{PodSelector: &api.LabelSelector{MatchLabels: map[string]string{"app": "api"}}}},
				Ports: []api.NetworkPolicyPort{{Protocol: api.ProtocolTCP, Port: 8080}},
			}},
		},
	}
}

func TestNetworkPolicyValidate(t *testing.T) {
	ipBlock := func(cidr string, except ...string) []api.NetworkPolicyPeer {
		return []api.NetworkPolicyPeer{{IPBlock: &api.IPBlock{CIDR: cidr, Except: except}}}
	}
	egress := func(peers []api.NetworkPolicyPeer) []api.EgressRule { return []api.EgressRule{{To: peers}} }
	tests := map[string]struct {
		mutate func(p *api.NetworkPolicy)
		valid  bool
	}{
		"complete":             {func(p *api.NetworkPolicy) {}, true},
		"wrong kind":           {func(p *api.NetworkPolicy) { p.Kind = api.KindService }, false},
		"wrong group version":  {func(p *api.NetworkPolicy) { p.APIVersion = "v1" }, false},
		"empty name":           {func(p *api.NetworkPolicy) { p.Metadata.Name = "" }, false},
		"name not a subdomain": {func(p *api.NetworkPolicy) { p.Metadata.Name = "Bad_Name" }, false},
		"invalid metadata":     {func(p *api.NetworkPolicy) { p.Metadata.Labels = map[string]string{"bad key!": "v"} }, false},

		"empty podSelector selects all": {func(p *api.NetworkPolicy) { p.Spec.PodSelector = api.LabelSelector{} }, true},
		"podSelector with a bad key": {func(p *api.NetworkPolicy) {
			p.Spec.PodSelector = api.LabelSelector{MatchLabels: map[string]string{"bad key!": "v"}}
		}, false},
		"podSelector with a bad operator": {func(p *api.NetworkPolicy) { p.Spec.PodSelector = expr("a", "Gt", "1") }, false},

		"no rules":                      {func(p *api.NetworkPolicy) { p.Spec.Ingress = nil }, true},
		"policyTypes Ingress":           {func(p *api.NetworkPolicy) { p.Spec.PolicyTypes = []api.PolicyType{api.PolicyIngress} }, true},
		"policyTypes both":              {func(p *api.NetworkPolicy) { p.Spec.PolicyTypes = []api.PolicyType{api.PolicyIngress, api.PolicyEgress} }, true},
		"policyTypes unknown":           {func(p *api.NetworkPolicy) { p.Spec.PolicyTypes = []api.PolicyType{"Both"} }, false},
		"policyTypes unknown, no rules": {func(p *api.NetworkPolicy) { p.Spec.Ingress = nil; p.Spec.PolicyTypes = []api.PolicyType{"Both"} }, false},
		"policyTypes repeated": {func(p *api.NetworkPolicy) {
			p.Spec.PolicyTypes = []api.PolicyType{api.PolicyIngress, api.PolicyIngress}
		}, false},
		"ingress rules, Egress only": {func(p *api.NetworkPolicy) { p.Spec.PolicyTypes = []api.PolicyType{api.PolicyEgress} }, false},
		"egress rules, Ingress only": {func(p *api.NetworkPolicy) {
			p.Spec.PolicyTypes = []api.PolicyType{api.PolicyIngress}
			p.Spec.Egress = egress(nil)
		}, false},
		"egress rules, types unset": {func(p *api.NetworkPolicy) { p.Spec.Egress = egress(nil) }, true},
		"egress rule on Egress only": {func(p *api.NetworkPolicy) {
			p.Spec.PolicyTypes = []api.PolicyType{api.PolicyEgress}
			p.Spec.Ingress = nil
			p.Spec.Egress = egress(nil)
		}, true},
		"Egress only with no rules": {func(p *api.NetworkPolicy) {
			p.Spec.PolicyTypes = []api.PolicyType{api.PolicyEgress}
			p.Spec.Ingress = nil
		}, true},
		"rule that allows everything": {func(p *api.NetworkPolicy) { p.Spec.Ingress = []api.IngressRule{{}} }, true},

		"peer with neither": {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = []api.NetworkPolicyPeer{{}} }, false},
		"peer with both":    {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From[0].IPBlock = &api.IPBlock{CIDR: "10.0.0.0/8"} }, false},
		"peer with an empty podSelector": {func(p *api.NetworkPolicy) {
			p.Spec.Ingress[0].From = []api.NetworkPolicyPeer{{PodSelector: &api.LabelSelector{}}}
		}, true},
		"peer with a bad podSelector": {func(p *api.NetworkPolicy) {
			s := expr("a", api.LabelSelectorIn)
			p.Spec.Ingress[0].From[0].PodSelector = &s
		}, false},
		"egress peer with neither": {func(p *api.NetworkPolicy) { p.Spec.Egress = egress([]api.NetworkPolicyPeer{{}}) }, false},
		"egress peer ipBlock":      {func(p *api.NetworkPolicy) { p.Spec.Egress = egress(ipBlock("10.0.0.0/8")) }, true},

		"ipBlock IPv4":           {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("192.168.1.0/24") }, true},
		"ipBlock IPv6":           {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("fd00::/64") }, true},
		"ipBlock a single host":  {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("192.168.1.5/32") }, true},
		"ipBlock empty":          {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("") }, false},
		"ipBlock an address":     {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("192.168.1.5") }, false},
		"ipBlock with host bits": {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("192.168.1.5/24") }, false},
		"ipBlock except inside": {func(p *api.NetworkPolicy) {
			p.Spec.Ingress[0].From = ipBlock("10.0.0.0/8", "10.1.0.0/16", "10.2.0.0/16")
		}, true},
		"except the same prefix":   {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("10.0.0.0/8", "10.0.0.0/8") }, false},
		"except wider":             {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("10.1.0.0/16", "10.0.0.0/8") }, false},
		"except outside":           {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("10.0.0.0/8", "192.168.0.0/16") }, false},
		"except of another family": {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("10.0.0.0/8", "fd00::/64") }, false},
		"except not a CIDR":        {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("10.0.0.0/8", "10.1.0.1") }, false},
		"except with host bits":    {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].From = ipBlock("10.0.0.0/8", "10.1.0.5/16") }, false},

		"port TCP":            {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Protocol = api.ProtocolTCP }, true},
		"port UDP":            {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Protocol = api.ProtocolUDP }, true},
		"port protocol SCTP":  {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Protocol = "SCTP" }, false},
		"port protocol empty": {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Protocol = "" }, false},
		"port 0":              {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Port = 0 }, false},
		"port 65536":          {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Port = 65536 }, false},
		"port 65535":          {func(p *api.NetworkPolicy) { p.Spec.Ingress[0].Ports[0].Port = 65535 }, true},
		"egress port 0": {func(p *api.NetworkPolicy) {
			p.Spec.Egress = []api.EgressRule{{Ports: []api.NetworkPolicyPort{{Protocol: api.ProtocolTCP}}}}
		}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := newNetworkPolicy("allow-api")
			tt.mutate(p)
			err := p.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestNetworkPolicySpecEffectivePolicyTypes(t *testing.T) {
	tests := map[string]struct {
		spec api.NetworkPolicySpec
		want []api.PolicyType
	}{
		"unset, no rules":      {api.NetworkPolicySpec{}, []api.PolicyType{api.PolicyIngress}},
		"unset, ingress rules": {api.NetworkPolicySpec{Ingress: []api.IngressRule{{}}}, []api.PolicyType{api.PolicyIngress}},
		"unset, egress rules":  {api.NetworkPolicySpec{Egress: []api.EgressRule{{}}}, []api.PolicyType{api.PolicyIngress, api.PolicyEgress}},
		"Egress given":         {api.NetworkPolicySpec{PolicyTypes: []api.PolicyType{api.PolicyEgress}}, []api.PolicyType{api.PolicyEgress}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.spec.EffectivePolicyTypes())
		})
	}
}

func TestClientNetworkPolicyPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetNetworkPolicy(ctx, "allow-api")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutNetworkPolicy(ctx, newNetworkPolicy("allow-api"))
	require.NoError(t, err)

	bad := newNetworkPolicy("bad")
	bad.Spec.Ingress[0].Ports[0].Port = 0
	_, err = c.PutNetworkPolicy(ctx, bad)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.GetNetworkPolicy(ctx, "allow-api")
	require.NoError(t, err)
	assert.Equal(t, int32(8080), got.Spec.Ingress[0].Ports[0].Port)

	list, err := c.ListNetworkPolicies(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindNetworkPolicyList, list.Kind)
	require.Len(t, list.Items, 1)

	w, err := c.WatchNetworkPolicies(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	assert.Equal(t, api.EventAdded, nextEvent(t, w).Type)

	_, err = c.DeleteNetworkPolicy(ctx, "allow-api", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetNetworkPolicy(ctx, "allow-api")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

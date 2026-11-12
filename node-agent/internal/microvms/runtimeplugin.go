package microvms

import (
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

var protocolToRuntime = map[plugin.Protocol]v1alpha2.Protocol{
	plugin.TCP:    v1alpha2.Protocol_PROTOCOL_TCP,
	plugin.UDP:    v1alpha2.Protocol_PROTOCOL_UDP,
	plugin.ICMP:   v1alpha2.Protocol_PROTOCOL_ICMP,
	plugin.ICMPv6: v1alpha2.Protocol_PROTOCOL_ICMPV6,
	plugin.Any:    v1alpha2.Protocol_PROTOCOL_ANY,
}

// policyToRuntime is the policy a plugin returned as mvmd's. A nil policy and
// an empty one both block all traffic. A protocol the plugin API does not
// name is left unspecified, which mvmd rejects.
func policyToRuntime(p *plugin.NetworkPolicy) *v1alpha2.NetworkPolicy {
	out := &v1alpha2.NetworkPolicy{}
	if p == nil {
		return out
	}
	rules := func(in []plugin.NetworkRule) []*v1alpha2.NetworkRule {
		var rs []*v1alpha2.NetworkRule
		for _, r := range in {
			rule := &v1alpha2.NetworkRule{Prefix: r.Prefix, Protocol: protocolToRuntime[r.Protocol]}
			for _, pr := range r.Ports {
				rule.Ports = append(rule.Ports, &v1alpha2.PortRange{First: pr.First, Last: pr.Last})
			}
			rs = append(rs, rule)
		}
		return rs
	}
	out.Egress, out.Ingress = rules(p.Egress), rules(p.Ingress)
	return out
}

func disksToRuntime(in []plugin.DiskAttachment) []*v1alpha2.DiskAttachment {
	var out []*v1alpha2.DiskAttachment
	for _, d := range in {
		out = append(out, &v1alpha2.DiskAttachment{DiskId: d.DiskID, ReadOnly: d.ReadOnly})
	}
	return out
}

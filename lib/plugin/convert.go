package plugin

import (
	pb "github.com/garamsh/gaon/lib/plugin/v1alpha1"
)

var protocolToWire = map[Protocol]pb.Protocol{
	TCP:    pb.Protocol_PROTOCOL_TCP,
	UDP:    pb.Protocol_PROTOCOL_UDP,
	ICMP:   pb.Protocol_PROTOCOL_ICMP,
	ICMPv6: pb.Protocol_PROTOCOL_ICMPV6,
	Any:    pb.Protocol_PROTOCOL_ANY,
}

func policyToWire(p *NetworkPolicy) *pb.NetworkPolicy {
	if p == nil {
		return &pb.NetworkPolicy{}
	}
	return &pb.NetworkPolicy{Egress: rulesToWire(p.Egress), Ingress: rulesToWire(p.Ingress)}
}

func rulesToWire(rules []NetworkRule) []*pb.NetworkRule {
	var out []*pb.NetworkRule
	for _, r := range rules {
		w := &pb.NetworkRule{Prefix: r.Prefix, Protocol: protocolToWire[r.Protocol]}
		for _, p := range r.Ports {
			w.Ports = append(w.Ports, &pb.PortRange{First: p.First, Last: p.Last})
		}
		out = append(out, w)
	}
	return out
}

func policyFromWire(p *pb.NetworkPolicy) *NetworkPolicy {
	return &NetworkPolicy{Egress: rulesFromWire(p.GetEgress()), Ingress: rulesFromWire(p.GetIngress())}
}

func rulesFromWire(rules []*pb.NetworkRule) []NetworkRule {
	var out []NetworkRule
	for _, w := range rules {
		r := NetworkRule{Prefix: w.GetPrefix()}
		for p, wp := range protocolToWire {
			if wp == w.GetProtocol() {
				r.Protocol = p
			}
		}
		for _, p := range w.GetPorts() {
			r.Ports = append(r.Ports, PortRange{First: p.GetFirst(), Last: p.GetLast()})
		}
		out = append(out, r)
	}
	return out
}

func refusalToWire(r *Refusal) *pb.Refusal {
	return &pb.Refusal{Reason: r.Reason, Message: r.Message}
}

func refusalFromWire(w *pb.Refusal) *Refusal {
	return &Refusal{Reason: w.GetReason(), Message: w.GetMessage()}
}

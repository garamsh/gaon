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
		r.Protocol = protocolFromWire(w.GetProtocol())
		for _, p := range w.GetPorts() {
			r.Ports = append(r.Ports, PortRange{First: p.GetFirst(), Last: p.GetLast()})
		}
		out = append(out, r)
	}
	return out
}

func limitsToWire(in []MicroVMLimits) []*pb.MicroVMLimits {
	var out []*pb.MicroVMLimits
	for _, l := range in {
		out = append(out, &pb.MicroVMLimits{Microvm: l.MicroVM, Addresses: l.Addresses, Ingress: limitToWire(l.Ingress), Egress: limitToWire(l.Egress)})
	}
	return out
}

func limitToWire(l *Limit) *pb.Limit {
	if l == nil {
		return nil
	}
	out := &pb.Limit{}
	for _, a := range l.Allow {
		w := &pb.Allow{Prefix: a.Prefix, Except: a.Except}
		for _, p := range a.Ports {
			w.Ports = append(w.Ports, &pb.PolicyPort{Protocol: protocolToWire[p.Protocol], Port: p.Port})
		}
		out.Allow = append(out.Allow, w)
	}
	return out
}

func limitsFromWire(in []*pb.MicroVMLimits) []MicroVMLimits {
	var out []MicroVMLimits
	for _, w := range in {
		out = append(out, MicroVMLimits{MicroVM: w.GetMicrovm(), Addresses: w.GetAddresses(), Ingress: limitFromWire(w.GetIngress()), Egress: limitFromWire(w.GetEgress())})
	}
	return out
}

func limitFromWire(w *pb.Limit) *Limit {
	if w == nil {
		return nil
	}
	out := &Limit{}
	for _, wa := range w.GetAllow() {
		a := Allow{Prefix: wa.GetPrefix(), Except: wa.GetExcept()}
		for _, wp := range wa.GetPorts() {
			a.Ports = append(a.Ports, PolicyPort{Protocol: protocolFromWire(wp.GetProtocol()), Port: wp.GetPort()})
		}
		out.Allow = append(out.Allow, a)
	}
	return out
}

func protocolFromWire(w pb.Protocol) Protocol {
	for p, wp := range protocolToWire {
		if wp == w {
			return p
		}
	}
	return 0
}

func refusalToWire(r *Refusal) *pb.Refusal {
	return &pb.Refusal{Reason: r.Reason, Message: r.Message}
}

func refusalFromWire(w *pb.Refusal) *Refusal {
	return &Refusal{Reason: w.GetReason(), Message: w.GetMessage()}
}

// Package network holds the built-in network plugins of the Node Agent.
package network

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Name is the network the Default plugin serves and the name that selects it.
const Name = "default"

// ReasonUnknownNetwork is the reason of a Machine that names a network the
// plugin does not serve.
const ReasonUnknownNetwork = "UnknownNetwork"

// Default serves the network "default": egress to the operator's prefixes,
// and ingress to a Machine's ports from the operator's prefixes. With no
// prefixes the policy is empty, which mvmd reads as blocking all outside
// traffic.
type Default struct {
	egress, ingress []netip.Prefix
}

// NewDefault returns a Default that allows egress to the prefixes in egress
// and ingress from those in ingress, each an IPv4 or IPv6 CIDR.
func NewDefault(egress, ingress []string) (*Default, error) {
	e, err := parsePrefixes("egress", egress)
	if err != nil {
		return nil, err
	}
	i, err := parsePrefixes("ingress", ingress)
	if err != nil {
		return nil, err
	}
	return &Default{egress: e, ingress: i}, nil
}

func parsePrefixes(direction string, in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%s prefix %q is not an IPv4 or IPv6 CIDR: %w", direction, s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Policy implements machines.Network. A Machine naming a network other than
// "default" is refused with ReasonUnknownNetwork, and one naming a port
// outside 1 to 65535, which mvmd would refuse, with machines.ReasonInvalidSpec.
func (d *Default) Policy(_ context.Context, m *api.Machine) (*v1alpha2.NetworkPolicy, error) {
	if i := slices.IndexFunc(m.Spec.Networks, func(n string) bool { return n != Name }); i >= 0 {
		return nil, &machines.Refusal{
			Reason:  ReasonUnknownNetwork,
			Message: fmt.Sprintf("spec.networks names %q; this node serves only %q", m.Spec.Networks[i], Name),
		}
	}
	for _, port := range m.Spec.Ports {
		if port < 1 || port > 65535 {
			return nil, &machines.Refusal{
				Reason:  machines.ReasonInvalidSpec,
				Message: fmt.Sprintf("spec.ports holds %d, want 1 to 65535", port),
			}
		}
	}
	policy := &v1alpha2.NetworkPolicy{}
	for _, p := range d.egress {
		policy.Egress = append(policy.Egress, &v1alpha2.NetworkRule{Prefix: p.String(), Protocol: v1alpha2.Protocol_PROTOCOL_ANY})
	}
	for _, port := range m.Spec.Ports {
		for _, p := range d.ingress {
			policy.Ingress = append(policy.Ingress, &v1alpha2.NetworkRule{
				Prefix:   p.String(),
				Protocol: v1alpha2.Protocol_PROTOCOL_TCP,
				Ports:    []*v1alpha2.PortRange{{First: uint32(port), Last: uint32(port)}},
			})
		}
	}
	return policy, nil
}

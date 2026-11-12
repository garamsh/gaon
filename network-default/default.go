// Package networkdefault is the network plugin "default": egress to the
// operator's prefixes, and ingress to a MicroVM's ports from the operator's
// prefixes. It is built on lib/plugin and lib/api and nothing else of gaon.
package networkdefault

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
)

// Name is the network the plugin serves and the name it is served under.
const Name = "default"

// The reasons a MicroVM fails with when Default refuses it.
const (
	// ReasonUnknownNetwork is the reason of a MicroVM that names a network
	// the plugin does not serve.
	ReasonUnknownNetwork = "UnknownNetwork"
	// ReasonInvalidSpec is the reason of a MicroVM whose spec the plugin
	// cannot make a policy of.
	ReasonInvalidSpec = "InvalidSpec"
)

// Default serves the network "default". With no prefixes its policy is empty,
// which mvmd reads as blocking all outside traffic.
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

// Policy implements plugin.Network. A MicroVM naming a network other than
// "default" is refused with ReasonUnknownNetwork, and one naming a port
// outside 1 to 65535, which mvmd would refuse, with ReasonInvalidSpec.
func (d *Default) Policy(_ context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	if i := slices.IndexFunc(m.Spec.Networks, func(n string) bool { return n != Name }); i >= 0 {
		return nil, &plugin.Refusal{
			Reason:  ReasonUnknownNetwork,
			Message: fmt.Sprintf("spec.networks names %q; this node serves only %q", m.Spec.Networks[i], Name),
		}
	}
	for _, port := range m.Spec.Ports {
		if port < 1 || port > 65535 {
			return nil, &plugin.Refusal{
				Reason:  ReasonInvalidSpec,
				Message: fmt.Sprintf("spec.ports holds %d, want 1 to 65535", port),
			}
		}
	}
	policy := &plugin.NetworkPolicy{}
	for _, p := range d.egress {
		policy.Egress = append(policy.Egress, plugin.NetworkRule{Prefix: p.String(), Protocol: plugin.Any})
	}
	for _, port := range m.Spec.Ports {
		for _, p := range d.ingress {
			policy.Ingress = append(policy.Ingress, plugin.NetworkRule{
				Prefix:   p.String(),
				Protocol: plugin.TCP,
				Ports:    []plugin.PortRange{{First: uint32(port), Last: uint32(port)}},
			})
		}
	}
	return policy, nil
}

package microvms

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
)

// enforce passes the effective NetworkPolicy limits of the MicroVMs to run
// here to the network plugin, when it enforces them. A plugin that does not
// (it answers Unimplemented, or is not an Enforcer) is left alone: the
// policy it returns at create time is all it applies.
func (s *service) enforce(ctx context.Context) error {
	e, ok := s.network.(plugin.Enforcer)
	if !ok {
		return nil
	}
	list, err := s.api.ListNetworkPolicies(ctx)
	if err != nil {
		return fmt.Errorf("list networkpolicies: %w", err)
	}
	limits, err := effectiveLimits(s.cache, list.Items)
	if err != nil {
		return err
	}
	if err := e.Enforce(ctx, limits); err != nil {
		if status.Code(err) == codes.Unimplemented {
			s.log.Debug("the network plugin does not enforce NetworkPolicy", "error", err)
			return nil
		}
		return fmt.Errorf("network plugin enforce: %w", err)
	}
	return nil
}

// effectiveLimits returns, for each MicroVM to run here that has an address,
// what the policies that select it allow in each direction. A direction no
// policy selects is nil, which is unrestricted; a selected one allows the
// union of what every selecting policy allows.
func effectiveLimits(c *cache, policies []api.NetworkPolicy) ([]plugin.MicroVMLimits, error) {
	var out []plugin.MicroVMLimits
	for _, name := range c.names() {
		m := c.microvms[name]
		if !c.mine(m) || len(m.Status.Addresses) == 0 {
			continue
		}
		l := plugin.MicroVMLimits{MicroVM: name}
		for _, a := range m.Status.Addresses {
			addr, err := addressOf(a)
			if err != nil {
				return nil, fmt.Errorf("microvm %s: %w", name, err)
			}
			l.Addresses = append(l.Addresses, addr.String())
		}
		for i := range policies {
			p := &policies[i]
			sel, err := p.Spec.PodSelector.Selector()
			if err != nil {
				return nil, fmt.Errorf("networkpolicy %s: %w", p.Metadata.Name, err)
			}
			if !sel.Matches(m.Metadata.Labels) {
				continue
			}
			for _, t := range p.Spec.EffectivePolicyTypes() {
				switch t {
				case api.PolicyIngress:
					if l.Ingress == nil {
						l.Ingress = &plugin.Limit{}
					}
					for _, r := range p.Spec.Ingress {
						a, err := allows(c, r.From, r.Ports)
						if err != nil {
							return nil, fmt.Errorf("networkpolicy %s: %w", p.Metadata.Name, err)
						}
						l.Ingress.Allow = append(l.Ingress.Allow, a...)
					}
				case api.PolicyEgress:
					if l.Egress == nil {
						l.Egress = &plugin.Limit{}
					}
					for _, r := range p.Spec.Egress {
						a, err := allows(c, r.To, r.Ports)
						if err != nil {
							return nil, fmt.Errorf("networkpolicy %s: %w", p.Metadata.Name, err)
						}
						l.Egress.Allow = append(l.Egress.Allow, a...)
					}
				}
			}
		}
		out = append(out, l)
	}
	return out, nil
}

// allows returns the Allows of one rule: one for each address an IPBlock or a
// selected MicroVM gives, or one for every address when the rule has no peer.
func allows(c *cache, peers []api.NetworkPolicyPeer, ports []api.NetworkPolicyPort) ([]plugin.Allow, error) {
	var pp []plugin.PolicyPort
	for _, p := range ports {
		proto := plugin.TCP
		if p.Protocol == api.ProtocolUDP {
			proto = plugin.UDP
		}
		pp = append(pp, plugin.PolicyPort{Protocol: proto, Port: uint32(p.Port)})
	}
	if len(peers) == 0 {
		return []plugin.Allow{{Ports: pp}}, nil
	}
	var out []plugin.Allow
	for _, peer := range peers {
		switch {
		case peer.IPBlock != nil:
			out = append(out, plugin.Allow{Prefix: peer.IPBlock.CIDR, Except: peer.IPBlock.Except, Ports: pp})
		case peer.PodSelector != nil:
			sel, err := peer.PodSelector.Selector()
			if err != nil {
				return nil, err
			}
			for _, name := range c.names() {
				m := c.microvms[name]
				if m.Metadata.DeletionTimestamp != nil || !sel.Matches(m.Metadata.Labels) {
					continue
				}
				for _, a := range m.Status.Addresses {
					addr, err := addressOf(a)
					if err != nil {
						return nil, fmt.Errorf("microvm %s: %w", name, err)
					}
					out = append(out, plugin.Allow{Prefix: netip.PrefixFrom(addr, addr.BitLen()).String(), Ports: pp})
				}
			}
		default:
			return nil, errors.New("a peer names neither a podSelector nor an ipBlock")
		}
	}
	return out, nil
}

// addressOf reads a MicroVM's address, which may carry a prefix length.
func addressOf(s string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("address %q is neither an address nor a prefix", s)
	}
	return p.Addr().Unmap(), nil
}

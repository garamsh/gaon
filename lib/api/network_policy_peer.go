package api

import (
	"fmt"
	"net/netip"
)

// validatePeers checks that every peer has exactly one of podSelector and
// ipBlock, and that each is valid. field names the list in messages.
func validatePeers(field string, peers []NetworkPolicyPeer) error {
	for i, p := range peers {
		at := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case p.PodSelector != nil && p.IPBlock != nil:
			return invalid("%s has both podSelector and ipBlock, want one", at)
		case p.PodSelector == nil && p.IPBlock == nil:
			return invalid("%s has neither podSelector nor ipBlock, want one", at)
		case p.PodSelector != nil:
			if _, err := p.PodSelector.Selector(); err != nil {
				return invalid("%s.podSelector: %v", at, err)
			}
		default:
			if err := p.IPBlock.validate(at + ".ipBlock"); err != nil {
				return err
			}
		}
	}
	return nil
}

// validate checks that the CIDR parses and is in canonical form, and that
// every except prefix is the same family, inside it and narrower.
func (b *IPBlock) validate(field string) error {
	cidr, err := netip.ParsePrefix(b.CIDR)
	if err != nil || cidr.Addr().Zone() != "" || cidr != cidr.Masked() {
		return invalid("%s.cidr is %q, want a CIDR with no host bits set", field, b.CIDR)
	}
	for i, e := range b.Except {
		ex, err := netip.ParsePrefix(e)
		if err != nil || ex.Addr().Zone() != "" || ex != ex.Masked() {
			return invalid("%s.except[%d] is %q, want a CIDR with no host bits set", field, i, e)
		}
		if !cidr.Contains(ex.Addr()) || ex.Bits() <= cidr.Bits() {
			return invalid("%s.except[%d] is %q, want a prefix inside %s and narrower than it", field, i, e, b.CIDR)
		}
	}
	return nil
}

// validatePolicyPorts checks the protocol and the port of every port.
func validatePolicyPorts(field string, ports []NetworkPolicyPort) error {
	for i, p := range ports {
		if p.Protocol != ProtocolTCP && p.Protocol != ProtocolUDP {
			return invalid("%s[%d].protocol is %q, want TCP or UDP", field, i, p.Protocol)
		}
		if p.Port < 1 || p.Port > 65535 {
			return invalid("%s[%d].port is %d, want 1 to 65535", field, i, p.Port)
		}
	}
	return nil
}

package networkdefault

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"github.com/garamsh/gaon/lib/plugin"
)

// TableName is the nftables table Enforce owns, of family inet. The plugin
// replaces it whole and touches no other table.
const TableName = "gaon_network_default"

// Enforce implements plugin.Enforcer. It replaces the table TableName with one
// whose forward hook limits the traffic to and from each MicroVM in limits,
// in one transaction, so a packet meets the old rules or the new and never a
// mix. Traffic already in a connection that a MicroVM's policy allowed to
// begin is not limited, in either direction.
func (d *Default) Enforce(_ context.Context, limits []plugin.MicroVMLimits) error {
	flows, err := compile(limits)
	if err != nil {
		return err
	}
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open netlink: %w", err)
	}
	defer func() { _ = conn.CloseLasting() }()
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: TableName}
	// Adding first makes the delete succeed when the table is not there yet.
	conn.AddTable(table)
	conn.DelTable(table)
	table = conn.AddTable(table)
	accept := nftables.ChainPolicyAccept
	forward := conn.AddChain(&nftables.Chain{
		Name: "forward", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &accept,
	})
	// A rule that allows ends its own chain with return, because accept would
	// end the hook and skip the other direction's chain.
	egress := conn.AddChain(&nftables.Chain{Name: "egress", Table: table})
	ingress := conn.AddChain(&nftables.Chain{Name: "ingress", Table: table})
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: []expr.Any{&expr.Verdict{Kind: expr.VerdictJump, Chain: egress.Name}}})
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: []expr.Any{&expr.Verdict{Kind: expr.VerdictJump, Chain: ingress.Name}}})
	for _, c := range []*nftables.Chain{egress, ingress} {
		conn.AddRule(&nftables.Rule{Table: table, Chain: c, Exprs: established()})
	}
	for _, f := range flows {
		chain := ingress
		if f.egress {
			chain = egress
		}
		for _, r := range f.rules() {
			conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: r})
		}
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("write table %s: %w", TableName, err)
	}
	return nil
}

// flow is the limit of one direction of one address of a MicroVM.
type flow struct {
	addr   netip.Addr
	egress bool
	allow  []allowed
}

// allowed is one prefix of an Allow, with its exceptions already cut out, and
// the ports it applies to.
type allowed struct {
	prefix netip.Prefix
	ports  []plugin.PolicyPort
}

// compile turns limits into one flow for each limited direction of each
// address. An Allow that names a prefix of the other family than the address
// cannot match its traffic and is left out.
func compile(limits []plugin.MicroVMLimits) ([]flow, error) {
	var flows []flow
	for _, l := range limits {
		for _, s := range l.Addresses {
			addr, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("microvm %s: address %q: %w", l.MicroVM, s, err)
			}
			addr = addr.Unmap()
			for _, dir := range []struct {
				limit  *plugin.Limit
				egress bool
			}{{l.Ingress, false}, {l.Egress, true}} {
				if dir.limit == nil {
					continue
				}
				f := flow{addr: addr, egress: dir.egress}
				for _, a := range dir.limit.Allow {
					for _, p := range a.Ports {
						if (p.Protocol != plugin.TCP && p.Protocol != plugin.UDP) || p.Port < 1 || p.Port > 65535 {
							return nil, fmt.Errorf("microvm %s: port %d of protocol %d, want TCP or UDP and 1 to 65535", l.MicroVM, p.Port, p.Protocol)
						}
					}
					prefixes, err := peerPrefixes(a, addr)
					if err != nil {
						return nil, fmt.Errorf("microvm %s: %w", l.MicroVM, err)
					}
					for _, p := range prefixes {
						f.allow = append(f.allow, allowed{prefix: p, ports: a.Ports})
					}
				}
				flows = append(flows, f)
			}
		}
	}
	return flows, nil
}

// peerPrefixes returns the prefixes of a's addresses that are of addr's
// family: the prefix less its exceptions, or every address when it has none.
func peerPrefixes(a plugin.Allow, addr netip.Addr) ([]netip.Prefix, error) {
	if a.Prefix == "" {
		return []netip.Prefix{netip.PrefixFrom(addr, 0).Masked()}, nil
	}
	p, err := netip.ParsePrefix(a.Prefix)
	if err != nil {
		return nil, fmt.Errorf("prefix %q: %w", a.Prefix, err)
	}
	p = p.Masked()
	if p.Addr().Is4() != addr.Is4() {
		return nil, nil
	}
	var except []netip.Prefix
	for _, s := range a.Except {
		e, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("except %q: %w", s, err)
		}
		except = append(except, e.Masked())
	}
	return subtract(p, except), nil
}

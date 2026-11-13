package networkdefault

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/garamsh/gaon/lib/plugin"
)

// ServicesTableName is the nftables table Expose owns, of family inet. The
// plugin replaces it whole and touches no table but this and TableName.
const ServicesTableName = "gaon_network_default_services"

// lanMark is the conntrack mark of a connection that came to a LAN address.
// The postrouting chain masquerades the connections that carry it, so that the
// replies of a backend on another node come back through this one.
const lanMark = 0x67616f6e

// The reject types and the ICMPX code of nf_tables.h, which x/sys/unix does
// not name.
const (
	rejectTCPReset     = 1
	rejectICMPXUnreach = 2
	icmpxPortUnreach   = 1
)

// Expose implements plugin.Exposer. It replaces the table ServicesTableName in
// one transaction, so a packet meets the old rules or the new and never a
// mix, then makes the node answer for the LAN addresses in announce and for
// no others. A route with backends sends a new connection to one of them, at
// random; a route with none refuses it with a TCP reset, or for UDP an ICMP
// port unreachable. A failure of either part is reported after the other ran.
func (d *Default) Expose(_ context.Context, routes []plugin.Route, announce []string) error {
	rs, err := compileRoutes(routes)
	if err != nil {
		return err
	}
	want, err := parseAnnounce(announce)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return errors.Join(writeServices(rs), d.lan.converge(want))
}

// route is a plugin.Route with its addresses parsed.
type route struct {
	addr     netip.Addr
	udp      bool
	port     uint16
	lan      bool
	backends []backend
}

type backend struct {
	addr netip.Addr
	port uint16
}

// compileRoutes parses routes. A backend of the other family than the
// route's address cannot be translated to and is left out, which leaves the
// route with none when all of them are.
func compileRoutes(in []plugin.Route) ([]route, error) {
	var out []route
	for _, r := range in {
		addr, err := netip.ParseAddr(r.Address)
		if err != nil {
			return nil, fmt.Errorf("service %s: address %q: %w", r.Service, r.Address, err)
		}
		addr = addr.Unmap()
		if (r.Protocol != plugin.TCP && r.Protocol != plugin.UDP) || r.Port < 1 || r.Port > 65535 {
			return nil, fmt.Errorf("service %s: port %d of protocol %d, want TCP or UDP and 1 to 65535", r.Service, r.Port, r.Protocol)
		}
		c := route{addr: addr, udp: r.Protocol == plugin.UDP, port: uint16(r.Port), lan: r.LAN}
		for _, b := range r.Backends {
			ba, err := netip.ParseAddr(b.Address)
			if err != nil {
				return nil, fmt.Errorf("service %s: backend %q: %w", r.Service, b.Address, err)
			}
			ba = ba.Unmap()
			if b.Port < 1 || b.Port > 65535 {
				return nil, fmt.Errorf("service %s: backend port %d, want 1 to 65535", r.Service, b.Port)
			}
			if ba.Is4() == addr.Is4() {
				c.backends = append(c.backends, backend{addr: ba, port: uint16(b.Port)})
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func parseAnnounce(in []string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range in {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("announce %q: %w", s, err)
		}
		out = append(out, a.Unmap())
	}
	return out, nil
}

func writeServices(routes []route) error {
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open netlink: %w", err)
	}
	defer func() { _ = conn.CloseLasting() }()
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: ServicesTableName}
	// Adding first makes the delete succeed when the table is not there yet.
	conn.AddTable(table)
	conn.DelTable(table)
	table = conn.AddTable(table)
	accept := nftables.ChainPolicyAccept
	pre := conn.AddChain(&nftables.Chain{
		Name: "prerouting", Table: table, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest, Policy: &accept,
	})
	post := conn.AddChain(&nftables.Chain{
		Name: "postrouting", Table: table, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource, Policy: &accept,
	})
	refuse := conn.AddChain(&nftables.Chain{
		Name: "refuse", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityRef(-50), Policy: &accept,
	})
	conn.AddRule(&nftables.Rule{Table: table, Chain: post, Exprs: lanReturn()})
	for _, r := range routes {
		if len(r.backends) == 0 {
			conn.AddRule(&nftables.Rule{Table: table, Chain: refuse, Exprs: r.refusal()})
			continue
		}
		for _, e := range r.translation() {
			conn.AddRule(&nftables.Rule{Table: table, Chain: pre, Exprs: e})
		}
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("write table %s: %w", ServicesTableName, err)
	}
	return nil
}

// match selects the packets to the address and port of r.
func (r route) match() []expr.Any {
	off, size := v4Dst, v4Len
	if r.addr.Is6() {
		off, size = v6Dst, v6Len
	}
	proto := byte(unix.IPPROTO_TCP)
	if r.udp {
		proto = unix.IPPROTO_UDP
	}
	return slices.Concat(family(r.addr), loadAddr(off, size), []expr.Any{
		equals(r.addr.AsSlice()),
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		equals([]byte{proto}),
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: dstPortOffset, Len: 2},
		equals(binary.BigEndian.AppendUint16(nil, r.port)),
	})
}

// translation returns one rule for each backend of r. The rule of backend i
// of n takes the connection with probability 1/(n-i), so each backend ends up
// with 1/n; the last takes what is left.
func (r route) translation() [][]expr.Any {
	var out [][]expr.Any
	fam := byte(unix.NFPROTO_IPV4)
	if r.addr.Is6() {
		fam = unix.NFPROTO_IPV6
	}
	n := len(r.backends)
	for i, b := range r.backends {
		e := r.match()
		if i < n-1 {
			e = append(e,
				&expr.Numgen{Register: 1, Modulus: uint32(n - i), Type: unix.NFT_NG_RANDOM},
				equals(make([]byte, 4)),
			)
		}
		if r.lan {
			e = append(e,
				&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(lanMark)},
				&expr.Ct{Key: expr.CtKeyMARK, Register: 1, SourceRegister: true},
			)
		}
		e = append(e,
			&expr.Immediate{Register: 1, Data: b.addr.AsSlice()},
			&expr.Immediate{Register: 2, Data: binary.BigEndian.AppendUint16(nil, b.port)},
			&expr.NAT{Type: expr.NATTypeDestNAT, Family: uint32(fam), RegAddrMin: 1, RegProtoMin: 2},
			&expr.Verdict{Kind: expr.VerdictAccept},
		)
		out = append(out, e)
	}
	return out
}

// refusal answers a packet to r with a TCP reset, or for UDP an ICMP port
// unreachable.
func (r route) refusal() []expr.Any {
	rej := &expr.Reject{Type: rejectTCPReset}
	if r.udp {
		rej = &expr.Reject{Type: rejectICMPXUnreach, Code: icmpxPortUnreach}
	}
	return append(r.match(), rej)
}

// lanReturn masquerades the connections that came to a LAN address.
func lanReturn() []expr.Any {
	return []expr.Any{
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1},
		equals(binaryutil.NativeEndian.PutUint32(lanMark)),
		&expr.Masq{},
	}
}

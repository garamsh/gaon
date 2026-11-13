package networkdefault

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// lan is where the node answers for LAN addresses: it holds each address it
// answers for on Interface as a host address, which the kernel answers ARP and
// NDP for. Pool is the range the plugin owns on Interface; a host address in
// it that is not wanted is removed, and one outside it is left alone.
type lan struct {
	iface string
	pool  []netip.Prefix
}

// converge makes the host addresses of the pool on the interface exactly want:
// it removes those not in want and adds those missing. An address of want
// outside the pool, or any address with no interface, is an error and nothing
// is added.
func (l lan) converge(want []netip.Addr) error {
	if l.iface == "" {
		if len(want) > 0 {
			return fmt.Errorf("announce %s: no --lan-interface to answer on", want[0])
		}
		return nil
	}
	for _, a := range want {
		if !l.inPool(a) {
			return fmt.Errorf("announce %s: outside --lan-pool %v", a, l.pool)
		}
	}
	link, err := netlink.LinkByName(l.iface)
	if err != nil {
		return fmt.Errorf("lan interface %s: %w", l.iface, err)
	}
	have, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("list addresses of %s: %w", l.iface, err)
	}
	var errs []error
	held := map[netip.Addr]bool{}
	for _, h := range have {
		a, ok := netip.AddrFromSlice(h.IP)
		if !ok {
			continue
		}
		a = a.Unmap()
		ones, bits := h.Mask.Size()
		if ones != bits || !l.inPool(a) {
			continue
		}
		if slices.Contains(want, a) {
			held[a] = true
			continue
		}
		if err := netlink.AddrDel(link, &h); err != nil {
			errs = append(errs, fmt.Errorf("remove %s from %s: %w", a, l.iface, err))
		}
	}
	for _, a := range want {
		if held[a] {
			continue
		}
		held[a] = true
		bits := 32
		if a.Is6() {
			bits = 128
		}
		add := &netlink.Addr{
			IPNet: &net.IPNet{IP: a.AsSlice(), Mask: net.CIDRMask(bits, bits)},
			// A duplicate address check would hold the address back for a
			// second, and the node only adds an address a Service holds.
			Flags: unix.IFA_F_NODAD,
		}
		if err := netlink.AddrAdd(link, add); err != nil {
			errs = append(errs, fmt.Errorf("add %s to %s: %w", a, l.iface, err))
		}
	}
	return errors.Join(errs...)
}

func (l lan) inPool(a netip.Addr) bool {
	return slices.ContainsFunc(l.pool, func(p netip.Prefix) bool { return p.Contains(a) })
}

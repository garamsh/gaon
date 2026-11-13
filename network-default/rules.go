package networkdefault

import (
	"encoding/binary"
	"net/netip"
	"slices"

	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/garamsh/gaon/lib/plugin"
)

// Offsets and lengths, in bytes, of the addresses in an IPv4 and an IPv6
// header, and of the destination port in a TCP or UDP header.
const (
	v4Src, v4Dst, v4Len = 12, 16, 4
	v6Src, v6Dst, v6Len = 8, 24, 16
	dstPortOffset       = 2
)

// rules returns the rules of f: one that returns for each prefix and port
// the limit allows, then one that drops what is left to or from the address.
func (f flow) rules() [][]expr.Any {
	srcOff, dstOff, size := v4Src, v4Dst, v4Len
	if f.addr.Is6() {
		srcOff, dstOff, size = v6Src, v6Dst, v6Len
	}
	selfOff, peerOff := dstOff, srcOff
	if f.egress {
		selfOff, peerOff = srcOff, dstOff
	}
	self := slices.Concat(family(f.addr), loadAddr(selfOff, size), []expr.Any{equals(f.addr.AsSlice())})
	var out [][]expr.Any
	for _, a := range f.allow {
		base := slices.Concat(self, matchPrefix(peerOff, size, a.prefix))
		if len(a.ports) == 0 {
			out = append(out, slices.Concat(base, []expr.Any{&expr.Verdict{Kind: expr.VerdictReturn}}))
		}
		for _, p := range a.ports {
			out = append(out, slices.Concat(base, matchPort(p), []expr.Any{&expr.Verdict{Kind: expr.VerdictReturn}}))
		}
	}
	return append(out, slices.Concat(self, []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}}))
}

// family matches packets of the IP version of a.
func family(a netip.Addr) []expr.Any {
	proto := byte(unix.NFPROTO_IPV4)
	if a.Is6() {
		proto = unix.NFPROTO_IPV6
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

func loadAddr(offset, size int) []expr.Any {
	return []expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: uint32(offset), Len: uint32(size)}}
}

func equals(data []byte) *expr.Cmp {
	return &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: data}
}

// matchPrefix matches the address at offset when it is in p. The prefix of
// length 0 matches every address, which family already selected.
func matchPrefix(offset, size int, p netip.Prefix) []expr.Any {
	if p.Bits() == 0 {
		return nil
	}
	mask := make([]byte, size)
	for i := range mask {
		bits := min(8, max(0, p.Bits()-8*i))
		mask[i] = byte(0xff << (8 - bits))
	}
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: uint32(offset), Len: uint32(size)},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: uint32(size), Mask: mask, Xor: make([]byte, size)},
		equals(p.Addr().AsSlice()),
	}
}

// matchPort matches the destination port of a TCP or UDP packet.
func matchPort(p plugin.PolicyPort) []expr.Any {
	proto := byte(unix.IPPROTO_TCP)
	if p.Protocol == plugin.UDP {
		proto = unix.IPPROTO_UDP
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		equals([]byte{proto}),
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: dstPortOffset, Len: 2},
		equals(binary.BigEndian.AppendUint16(nil, uint16(p.Port))),
	}
}

// established returns for packets of a connection that was allowed to begin,
// so the replies of an allowed flow are not dropped by the other direction.
func established() []expr.Any {
	mask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	return []expr.Any{
		&expr.Ct{Key: expr.CtKeySTATE, Register: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
}

// subtract returns prefixes that together hold the addresses of p that no
// prefix of except holds.
func subtract(p netip.Prefix, except []netip.Prefix) []netip.Prefix {
	overlaps := false
	for _, e := range except {
		if e.Bits() <= p.Bits() && e.Contains(p.Addr()) {
			return nil
		}
		if p.Contains(e.Addr()) && e.Bits() > p.Bits() {
			overlaps = true
		}
	}
	if !overlaps {
		return []netip.Prefix{p}
	}
	lo := netip.PrefixFrom(p.Addr(), p.Bits()+1)
	hi := netip.PrefixFrom(upperHalf(p), p.Bits()+1)
	return append(subtract(lo, except), subtract(hi, except)...)
}

// upperHalf returns the first address of the upper half of p.
func upperHalf(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	b[p.Bits()/8] |= 0x80 >> (p.Bits() % 8)
	a, _ := netip.AddrFromSlice(b)
	return a
}

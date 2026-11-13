package zone

import (
	"net/netip"
	"regexp"
	"strings"

	"github.com/miekg/dns"
)

// microVMLabel is a MicroVM name that is also a DNS label.
var microVMLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Answer replies to the first question of req, which Covers. The reply is
// SERVFAIL until both kinds have been listed, NXDOMAIN for a name that no
// Service makes, and otherwise NOERROR with the records of the question's
// family, or with none.
func (z *Zone) Answer(req *dns.Msg) *dns.Msg {
	q := req.Question[0]
	reply := new(dns.Msg)
	reply.SetReply(req)
	reply.Authoritative = true

	z.mu.RLock()
	defer z.mu.RUnlock()
	if !z.servicesListed || !z.endpointsListed {
		reply.Rcode = dns.RcodeServerFailure
		return reply
	}
	addrs, found := z.addresses(dns.CanonicalName(q.Name))
	if !found {
		reply.Rcode = dns.RcodeNameError
		reply.Ns = []dns.RR{z.startOfAuthority()}
		return reply
	}
	for _, a := range addrs {
		switch {
		case q.Qtype == dns.TypeA && a.Is4():
			reply.Answer = append(reply.Answer, &dns.A{Hdr: z.header(q.Name, dns.TypeA), A: a.AsSlice()})
		case q.Qtype == dns.TypeAAAA && a.Is6():
			reply.Answer = append(reply.Answer, &dns.AAAA{Hdr: z.header(q.Name, dns.TypeAAAA), AAAA: a.AsSlice()})
		}
	}
	if len(reply.Answer) == 0 {
		reply.Ns = []dns.RR{z.startOfAuthority()}
	}
	return reply
}

func (z *Zone) header(name string, rrtype uint16) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: rrtype, Class: dns.ClassINET, Ttl: TTL}
}

func (z *Zone) startOfAuthority() dns.RR {
	return &dns.SOA{
		Hdr:     z.header(z.apex, dns.TypeSOA),
		Ns:      "ns.dns." + z.apex,
		Mbox:    "hostmaster." + z.apex,
		Serial:  1,
		Refresh: 7200,
		Retry:   1800,
		Expire:  86400,
		Minttl:  TTL,
	}
}

// addresses returns the addresses of every family that the lowercase name
// holds, and whether the name exists. The caller holds z.mu.
func (z *Zone) addresses(name string) (addrs []netip.Addr, found bool) {
	labels := strings.Split(strings.TrimSuffix(strings.TrimSuffix(name, z.apex), "."), ".")
	n := len(labels)
	if n < 3 || n > 4 || labels[n-1] != "svc" || labels[n-2] != Namespace {
		return nil, false
	}
	svc, ok := z.services[labels[n-3]]
	if !ok {
		return nil, false
	}
	if n == 3 {
		if !svc.Spec.Headless() {
			addr, err := netip.ParseAddr(svc.Spec.ClusterIP)
			if err != nil {
				return nil, true
			}
			return []netip.Addr{addr.Unmap()}, true
		}
		addrs = z.endpointAddresses(svc.Metadata.Name, "")
		return addrs, len(addrs) > 0
	}
	if !svc.Spec.Headless() || !microVMLabel.MatchString(labels[0]) {
		return nil, false
	}
	addrs = z.endpointAddresses(svc.Metadata.Name, labels[0])
	return addrs, len(addrs) > 0
}

// endpointAddresses lists the addresses in the Endpoints named service, each
// once, in the order they appear. A non-empty microVM keeps only those whose
// targetRef names it.
func (z *Zone) endpointAddresses(service, microVM string) []netip.Addr {
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	for _, subset := range z.endpoints[service].Subsets {
		for _, a := range subset.Addresses {
			if microVM != "" && (a.TargetRef == nil || a.TargetRef.Name != microVM) {
				continue
			}
			addr, err := netip.ParseAddr(a.IP)
			if err != nil {
				continue
			}
			if addr = addr.Unmap(); !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	return out
}

// Package forward sends a question to the upstream resolvers the unit is
// configured with and returns the upstream's reply.
package forward

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/miekg/dns"
)

// DefaultPort is the port of an upstream given as an address alone.
const DefaultPort = 53

// timeout bounds the wait for one upstream.
const timeout = 2 * time.Second

// Forwarder asks its upstreams in order, over the transport of the question.
type Forwarder struct {
	upstreams []netip.AddrPort
}

// New returns a Forwarder of upstreams. With none, Configured is false.
func New(upstreams []netip.AddrPort) *Forwarder {
	return &Forwarder{upstreams: upstreams}
}

// ParseUpstream reads an upstream written as an address, or as an address and
// a port, with the port DefaultPort when it is absent.
func ParseUpstream(text string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(text); err == nil {
		return ap, nil
	}
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("upstream %q is not an address or an address and a port", text)
	}
	return netip.AddrPortFrom(addr, DefaultPort), nil
}

// Configured reports whether there is an upstream to ask.
func (f *Forwarder) Configured() bool { return len(f.upstreams) > 0 }

// Exchange sends req unchanged to each upstream in turn over network, "udp" or
// "tcp", and returns the reply of the first that answers. It returns an error
// when none does.
func (f *Forwarder) Exchange(network string, req *dns.Msg) (*dns.Msg, error) {
	client := dns.Client{Net: network, Timeout: timeout}
	var errs []error
	for _, upstream := range f.upstreams {
		reply, _, err := client.Exchange(req, upstream.String())
		if err == nil {
			return reply, nil
		}
		errs = append(errs, fmt.Errorf("upstream %s: %w", upstream, err))
	}
	return nil, errors.Join(errs...)
}

package server

import (
	"log/slog"

	"github.com/miekg/dns"

	"github.com/garamsh/gaon/dns/internal/forward"
	"github.com/garamsh/gaon/dns/internal/zone"
)

const (
	// plainUDPSize is the reply size of a UDP question that carries no EDNS0
	// buffer size.
	plainUDPSize = dns.MinMsgSize
	// maxUDPSize caps the buffer size a UDP question may ask for.
	maxUDPSize = 1232
)

// handler answers a question from the zone when its name is under it, and
// from the upstreams otherwise.
type handler struct {
	zone    *zone.Zone
	forward *forward.Forwarder
	log     *slog.Logger
}

// ServeDNS replies to req on w.
func (h *handler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	network := w.LocalAddr().Network()
	var reply *dns.Msg
	switch {
	case req.Opcode != dns.OpcodeQuery:
		reply = new(dns.Msg).SetRcode(req, dns.RcodeNotImplemented)
	case len(req.Question) != 1:
		reply = new(dns.Msg).SetRcode(req, dns.RcodeFormatError)
	case h.zone.Covers(req.Question[0].Name):
		reply = h.zone.Answer(req)
		fit(req, reply, network)
	case !h.forward.Configured():
		reply = new(dns.Msg).SetRcode(req, dns.RcodeRefused)
	default:
		var err error
		if reply, err = h.forward.Exchange(network, req); err != nil {
			h.log.Warn("forward", "name", req.Question[0].Name, "error", err)
			reply = new(dns.Msg).SetRcode(req, dns.RcodeServerFailure)
		}
	}
	if err := w.WriteMsg(reply); err != nil {
		h.log.Warn("write reply", "network", network, "error", err)
	}
}

// fit answers EDNS0 with EDNS0 and cuts a UDP reply to the size the question
// allows, setting the truncated bit.
func fit(req, reply *dns.Msg, network string) {
	size := plainUDPSize
	if opt := req.IsEdns0(); opt != nil {
		size = min(max(int(opt.UDPSize()), plainUDPSize), maxUDPSize)
		reply.SetEdns0(uint16(size), false)
	}
	if network == "udp" {
		reply.Truncate(size)
	}
}

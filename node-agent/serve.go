package nodeagent

import (
	"crypto/tls"
	"log/slog"

	"github.com/garamsh/gaon/node-agent/internal/certificate"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/node"
)

// servingCertificate returns the function that gives the certificate the agent
// serves with, and whether it can serve. An agent given no address does not
// serve, and says nothing. One that keeps its certificate in cfg.CertDir serves
// with it; one given a certificate checks it once and, when it cannot serve
// with it, logs why.
func servingCertificate(cfg Config, holder *certificate.Holder, clk clock.Clock, log *slog.Logger) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), bool) {
	if len(cfg.AgentAddresses) == 0 {
		return nil, false
	}
	if holder != nil {
		return holder.ServerCertificate, true
	}
	pair, err := certificate.ServingPair(cfg.TLSCert, cfg.TLSKey, cfg.TLSCA, cfg.NodeName, clk.Now())
	if err != nil {
		log.Error("the agent does not serve a MicroVM's log and exec: its certificate cannot serve", "certificate", cfg.TLSCert, "error", err)
		return nil, false
	}
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return pair, nil }, true
}

// reach is what the Node's status says about where the agent serves: the port
// only when it does.
func reach(cfg Config, serving bool) node.Reach {
	r := node.Reach{}
	for _, a := range cfg.AgentAddresses {
		r.Addresses = append(r.Addresses, a.String())
	}
	if serving {
		r.Port = cfg.AgentPort
	}
	return r
}

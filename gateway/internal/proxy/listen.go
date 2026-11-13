package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/garamsh/gaon/gateway/internal/gateway"
)

// errNoCertificate ends the handshake of a server name that has no certificate.
var errNoCertificate = errors.New("no certificate for the server name")

// portServer is what listens on one port.
type portServer struct {
	protocol string
	ln       net.Listener
	// srv serves an HTTP or HTTPS port; a TLS port has none.
	srv *http.Server

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// track records a passthrough connection, or refuses it when the port is closed.
func (s *portServer) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *portServer) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *portServer) close() {
	s.mu.Lock()
	s.closed = true
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	if s.srv != nil {
		_ = s.srv.Close()
	}
	_ = s.ln.Close()
	for c := range conns {
		_ = c.Close()
	}
}

// reconcile opens the ports the table serves and closes the ports it no longer
// serves, or serves with another protocol.
func (p *Proxy) reconcile(t *table) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	for port, s := range p.servers {
		if pt, ok := t.ports[port]; !ok || pt.protocol != s.protocol {
			s.close()
			delete(p.servers, port)
			p.log.Info("closed the port", "port", port)
		}
	}
	for port, pt := range t.ports {
		if _, ok := p.servers[port]; ok {
			continue
		}
		s, err := p.open(port, pt.protocol)
		if err != nil {
			p.log.Warn("cannot listen", "port", port, "error", err)
			continue
		}
		p.servers[port] = s
		p.log.Info("listening", "port", port, "protocol", pt.protocol)
	}
}

func (p *Proxy) open(port int32, protocol string) (*portServer, error) {
	raw, err := p.cfg.Listen(port)
	if err != nil {
		return nil, err
	}
	ln := idleListener{Listener: raw, idle: p.cfg.IdleTimeout}
	s := &portServer{protocol: protocol, ln: raw, conns: map[net.Conn]struct{}{}}
	switch protocol {
	case gateway.ProtocolHTTP:
		s.srv = p.newHTTPServer(port, false)
		go p.serve(port, s, ln)
	case gateway.ProtocolHTTPS:
		s.srv = p.newHTTPServer(port, true)
		cfg := &tls.Config{
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"http/1.1"},
			GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) { return p.certificateFor(port, h) },
		}
		go p.serve(port, s, tls.NewListener(ln, cfg))
	case gateway.ProtocolTLS:
		go p.acceptPassthrough(s, ln, port)
	default:
		_ = raw.Close()
		return nil, fmt.Errorf("listener protocol %q is not served", protocol)
	}
	return s, nil
}

func (p *Proxy) serve(port int32, s *portServer, ln net.Listener) {
	if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		p.stopped(port, s, err)
	}
}

// stopped logs a port whose listener failed and closes it, so the next Apply
// opens the port again. A port already replaced or closed is left alone.
func (p *Proxy) stopped(port int32, s *portServer, err error) {
	p.log.Warn("port stopped serving", "port", port, "error", err)
	p.mu.Lock()
	if p.servers[port] == s {
		delete(p.servers, port)
	}
	p.mu.Unlock()
	s.close()
}

// certificateFor returns the certificate of the listener the server name
// selects. The current table and the current certificate are read at each
// handshake, so a reloaded certificate serves the next connection and an open
// connection keeps the one it had.
func (p *Proxy) certificateFor(port int32, h *tls.ClientHelloInfo) (*tls.Certificate, error) {
	pt := p.table.Load().ports[port]
	if pt == nil {
		return nil, errNoCertificate
	}
	l := pt.listener(strings.ToLower(h.ServerName))
	if l == nil || l.secret == "" {
		return nil, errNoCertificate
	}
	cert := p.certs.get(l.secret)
	if cert == nil {
		return nil, errNoCertificate
	}
	return cert, nil
}

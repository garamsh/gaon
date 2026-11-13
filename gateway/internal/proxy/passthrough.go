package proxy

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

// errHello ends the handshake that only reads the server name.
var errHello = errors.New("client hello read")

// recordConn keeps what is read from it and discards what is written, so a
// handshake run over it sends the client nothing.
type recordConn struct {
	net.Conn
	read []byte
}

func (c *recordConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read = append(c.read, b[:n]...)
	return n, err
}

func (c *recordConn) Write(b []byte) (int, error) { return len(b), nil }

// serverName reads the ClientHello from c and returns its server name and the
// bytes read. It terminates nothing: the handshake stops at the hello.
func serverName(c net.Conn) (string, []byte) {
	rec := &recordConn{Conn: c}
	var name string
	cfg := &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			name = h.ServerName
			return nil, errHello
		},
	}
	_ = tls.Server(rec, cfg).Handshake()
	return strings.ToLower(name), rec.read
}

func (p *Proxy) acceptPassthrough(s *portServer, ln net.Listener, port int32) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if !s.track(c) {
			_ = c.Close()
			continue
		}
		go func() {
			defer s.untrack(c)
			p.passthrough(c, port)
		}()
	}
}

// passthrough routes the connection by its server name and copies bytes both
// ways. A connection with no server name, no route or no address is closed
// without a reply.
func (p *Proxy) passthrough(c net.Conn, port int32) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	name, hello := serverName(c)
	_ = c.SetReadDeadline(time.Time{})
	if name == "" {
		return
	}
	pt := p.table.Load().ports[port]
	if pt == nil {
		return
	}
	m, ok := pt.lookup(name, name, "/")
	if !ok {
		return
	}
	addr, ok := m.rule.backend.pick()
	if !ok {
		return
	}
	b, err := (&net.Dialer{Timeout: handshakeTimeout}).Dial("tcp", addr)
	if err != nil {
		p.log.Warn("backend failed", "backend", addr, "error", err)
		return
	}
	defer b.Close()
	if _, err := b.Write(hello); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, b); done <- struct{}{} }()
	<-done
}

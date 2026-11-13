// Package proxy serves the listeners and the routes of one Gateway: it routes
// HTTP and HTTPS requests to the Endpoints of a Service and passes TLS
// connections through by server name.
package proxy

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// The default times of a connection (ADR 0032).
const (
	// DefaultIdleTimeout closes a connection in which no byte moves for this long.
	DefaultIdleTimeout = time.Hour
	// DefaultKeepAlive closes a keep-alive connection that no request uses for this long.
	DefaultKeepAlive = 60 * time.Second
)

// handshakeTimeout bounds the wait for a request header or a ClientHello.
const handshakeTimeout = 10 * time.Second

// Config is how a Proxy runs.
type Config struct {
	// Gateway is the name of the Gateway the proxy serves.
	Gateway string
	// IdleTimeout and KeepAlive take their defaults when zero.
	IdleTimeout time.Duration
	KeepAlive   time.Duration
	// HelloTimeout is how long a TLS passthrough connection may take to send
	// its ClientHello; it takes handshakeTimeout when zero.
	HelloTimeout time.Duration
	// Listen opens the port of a listener; the default listens on every address.
	Listen func(port int32) (net.Listener, error)
}

// Objects is every object the proxy reads, as the API server holds it now.
type Objects struct {
	Gateways   []api.CustomObject
	HTTPRoutes []api.CustomObject
	TLSRoutes  []api.CustomObject
	Services   []api.Service
	Endpoints  []api.Endpoints
	// Secrets are the Secrets labelled for certificates.
	Secrets []api.Secret
}

// Proxy serves what the last Apply described.
type Proxy struct {
	cfg   Config
	log   *slog.Logger
	certs *certificates
	table atomic.Pointer[table]
	trans *http.Transport

	mu      sync.Mutex
	servers map[int32]*portServer
	closed  bool
}

// New returns a Proxy that serves nothing until Apply.
func New(cfg Config, log *slog.Logger) *Proxy {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = handshakeTimeout
	}
	if cfg.Listen == nil {
		cfg.Listen = func(port int32) (net.Listener, error) {
			return net.Listen("tcp", ":"+strconv.Itoa(int(port)))
		}
	}
	p := &Proxy{cfg: cfg, log: log, certs: newCertificates(log), trans: newTransport(), servers: map[int32]*portServer{}}
	p.table.Store(&table{ports: map[int32]*portTable{}})
	return p
}

// Apply makes the proxy serve objects: it swaps the table, loads the
// certificates, and opens and closes ports to the listeners the Gateway now
// has. A port that cannot be opened is logged and tried again by the next Apply.
func (p *Proxy) Apply(o Objects) {
	t := buildTable(p.cfg.Gateway, o)
	p.certs.sync(t.secrets(), o.Secrets)
	p.table.Store(t)
	p.reconcile(t)
}

// Close stops every listener and closes the connections that remain.
func (p *Proxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for port, s := range p.servers {
		s.close()
		delete(p.servers, port)
	}
	p.trans.CloseIdleConnections()
}

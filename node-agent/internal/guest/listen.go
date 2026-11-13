package guest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"time"
)

// APIServerOrganization is the Organization of the certificate an API server
// presents to the agent: a client certificate without it is refused.
const APIServerOrganization = "gaon:apiservers"

// shutdownGrace is how long Run waits for requests in flight once ctx ends.
const shutdownGrace = 5 * time.Second

// Listener serves Handler over mutual TLS at each of Addrs on Port.
type Listener struct {
	lns []net.Listener
	srv *http.Server
	log *slog.Logger
}

// ListenConfig is what a Listener needs.
type ListenConfig struct {
	Addrs []netip.Addr
	Port  int
	// CAFile holds the CA certificates a client certificate must chain to.
	CAFile string
	// Certificate returns the certificate the agent presents.
	Certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Handler     http.Handler
	Log         *slog.Logger
}

// Listen binds every address of cfg and returns the Listener that serves
// them. It binds all or none.
func Listen(cfg ListenConfig) (*Listener, error) {
	pem, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read the cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("the cluster CA file %s holds no certificate", cfg.CAFile)
	}
	tlsConfig := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool,
		GetCertificate: cfg.Certificate,
		VerifyConnection: func(cs tls.ConnectionState) error {
			for _, o := range cs.PeerCertificates[0].Subject.Organization {
				if o == APIServerOrganization {
					return nil
				}
			}
			return fmt.Errorf("the client certificate is not of %s", APIServerOrganization)
		},
	}
	l := &Listener{log: cfg.Log}
	for _, a := range cfg.Addrs {
		ln, err := net.Listen("tcp", net.JoinHostPort(a.String(), strconv.Itoa(cfg.Port)))
		if err != nil {
			l.close()
			return nil, fmt.Errorf("listen: %w", err)
		}
		l.lns = append(l.lns, tls.NewListener(ln, tlsConfig))
	}
	l.srv = &http.Server{
		Handler:           cfg.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(cfg.Log.Handler(), slog.LevelDebug),
	}
	return l, nil
}

// Addrs returns the addresses the Listener is bound to.
func (l *Listener) Addrs() []net.Addr {
	var out []net.Addr
	for _, ln := range l.lns {
		out = append(out, ln.Addr())
	}
	return out
}

func (l *Listener) close() {
	for _, ln := range l.lns {
		_ = ln.Close()
	}
}

// Run serves until ctx ends.
func (l *Listener) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, ln := range l.lns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				l.log.Error("the listener stopped", "address", ln.Addr().String(), "error", err)
			}
		}()
	}
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := l.srv.Shutdown(shut); err != nil {
		_ = l.srv.Close()
	}
	wg.Wait()
}

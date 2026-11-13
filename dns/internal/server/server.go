// Package server serves the names of Services over UDP and TCP, keeping the
// Services and Endpoints it answers from current through the resource API.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/garamsh/gaon/dns/internal/forward"
	"github.com/garamsh/gaon/dns/internal/zone"
	"github.com/garamsh/gaon/lib/api"
)

// Config is what the server is started with.
type Config struct {
	// Zone is the cluster domain, such as cluster.local.
	Zone string
	// Upstreams are the resolvers a name outside Zone is forwarded to.
	Upstreams []netip.AddrPort
	// APIServer is the base URL of the API server, and CAFile the file of the
	// certificate authority that signed its certificate.
	APIServer string
	CAFile    string
	// TokenFile holds the ServiceAccount token the requests carry.
	TokenFile string
	// RetryInterval is the wait before listing again after a failure.
	RetryInterval time.Duration
}

// Run answers on udp and tcp until ctx ends. It returns nil then, and the
// error of a server that stopped before.
func Run(ctx context.Context, cfg Config, udp net.PacketConn, tcp net.Listener, log *slog.Logger) error {
	z, err := zone.New(cfg.Zone)
	if err != nil {
		return err
	}
	httpClient, err := api.NewServerTLSClient(cfg.CAFile)
	if err != nil {
		return err
	}
	h := &handler{zone: z, forward: forward.New(cfg.Upstreams), log: log}
	started := make(chan struct{}, 2)
	notify := func() { started <- struct{}{} }
	servers := []*dns.Server{
		{PacketConn: udp, Handler: h, NotifyStartedFunc: notify},
		{Listener: tcp, Handler: h, NotifyStartedFunc: notify},
	}
	failed := make(chan error, len(servers))
	for _, s := range servers {
		go func() { failed <- s.ActivateAndServe() }()
	}
	var serveErr error
	for range servers {
		select {
		case <-started:
		case serveErr = <-failed:
		}
		if serveErr != nil {
			break
		}
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	src := &source{
		root:      strings.TrimSuffix(cfg.APIServer, "/") + "/apis/" + api.GroupVersion,
		http:      httpClient,
		tokenFile: cfg.TokenFile,
		retry:     cfg.RetryInterval,
		log:       log,
	}
	synced := make(chan struct{})
	go func() {
		defer close(synced)
		src.run(ctx, z)
	}()

	if serveErr == nil {
		select {
		case <-ctx.Done():
		case serveErr = <-failed:
		}
	}
	stop()
	for _, s := range servers {
		if err := s.Shutdown(); err != nil && serveErr == nil {
			serveErr = fmt.Errorf("shutdown: %w", err)
		}
	}
	<-synced
	return serveErr
}

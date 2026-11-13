// Command gaon-dns answers DNS questions for the Services of the resource API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/garamsh/gaon/dns/internal/forward"
	"github.com/garamsh/gaon/dns/internal/server"
)

// tokenPath is where the Node Agent writes the MicroVM's ServiceAccount token
// (ADR 0029).
const tokenPath = "/var/run/secrets/gaon.garam.sh/serviceaccount/token"

// retryInterval is the wait before the unit lists again after losing the API
// server.
const retryInterval = 2 * time.Second

// config is what the unit is started with.
type config struct {
	server server.Config
	listen string
}

// upstreams collects the values of a repeatable flag.
type upstreams []netip.AddrPort

func (u *upstreams) String() string { return fmt.Sprint([]netip.AddrPort(*u)) }

func (u *upstreams) Set(text string) error {
	ap, err := forward.ParseUpstream(text)
	if err != nil {
		return err
	}
	*u = append(*u, ap)
	return nil
}

// parseFlags reads the flags in args. It returns flag.ErrHelp when usage was
// asked for, after writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-dns", flag.ContinueOnError)
	fs.SetOutput(out)
	apiServer := fs.String("server", "", "base URL of the API server, such as https://127.0.0.1:8443")
	caFile := fs.String("tls-ca", "", "file of the certificate authority that signed the API server's certificate")
	tokenFile := fs.String("token-file", tokenPath, "file of the ServiceAccount token")
	zone := fs.String("zone", "cluster.local", "cluster domain whose Services are answered")
	listen := fs.String("listen", ":53", "address to answer on, over UDP and TCP")
	var upstream upstreams
	fs.Var(&upstream, "upstream", "resolver, as an address or an address and a port, that a name outside the zone is forwarded to; repeat for more; none refuses such names")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *apiServer == "" || *caFile == "" {
		return config{}, errors.New("flags -server and -tls-ca are required")
	}
	return config{
		listen: *listen,
		server: server.Config{
			Zone:          *zone,
			Upstreams:     upstream,
			APIServer:     *apiServer,
			CAFile:        *caFile,
			TokenFile:     *tokenFile,
			RetryInterval: retryInterval,
		},
	}, nil
}

func main() {
	log := slog.Default()
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Error("start", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log); err != nil {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

// run binds the listen address over UDP and TCP and serves until ctx ends.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	udp, err := net.ListenPacket("udp", cfg.listen)
	if err != nil {
		return err
	}
	defer udp.Close()
	tcp, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return err
	}
	defer tcp.Close()
	return server.Run(ctx, cfg.server, udp, tcp, log)
}

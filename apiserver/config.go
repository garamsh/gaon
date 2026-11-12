package apiserver

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"
)

// Config is how gaon-apiserver is run.
type Config struct {
	// Listen is the address the API is served on. Anything but a loopback
	// address needs the TLS files.
	Listen string
	// Endpoints are the etcd client endpoints of the cell's Store.
	Endpoints []string
	// DialTimeout bounds the connection to etcd at start.
	DialTimeout time.Duration
	// TLSClientCA, TLSCert and TLSKey are the paths of the CA that signs the
	// callers' certificates and of the server's certificate and key. All three
	// are set or none is.
	TLSClientCA, TLSCert, TLSKey string
	// BootstrapTokenFile is the path of the file of valid bootstrap tokens,
	// one per line. It needs the TLS files.
	BootstrapTokenFile string
	// AuthorizationMode is AlwaysAllow, the default where it is empty, or
	// RBAC. RBAC needs the TLS files.
	AuthorizationMode string
}

// The authorization modes of ADR 0008, Decision 2.
const (
	ModeAlwaysAllow = "AlwaysAllow"
	ModeRBAC        = "RBAC"
)

// validateAuthorization refuses a mode that is not one of the two, and RBAC
// without the TLS files, because a request with no client certificate has no
// identity to authorize.
func (c Config) validateAuthorization() error {
	switch c.AuthorizationMode {
	case "", ModeAlwaysAllow:
		return nil
	case ModeRBAC:
		given, err := c.tlsGiven()
		if err != nil {
			return err
		}
		if !given {
			return errors.New("authorization-mode RBAC needs tls-client-ca, tls-cert and tls-key: a request with no client certificate has no identity to authorize")
		}
		return nil
	}
	return fmt.Errorf("authorization-mode %q is not %s or %s", c.AuthorizationMode, ModeAlwaysAllow, ModeRBAC)
}

// ParseConfig reads the flags in args.
func ParseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("gaon-apiserver", flag.ContinueOnError)
	cfg := Config{}
	endpoints := fs.String("etcd-endpoints", "http://127.0.0.1:2379", "comma separated etcd client endpoints")
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "address to serve the API on; a non-loopback address needs the tls flags")
	fs.StringVar(&cfg.TLSClientCA, "tls-client-ca", "", "PEM file of the CA that signs client certificates")
	fs.StringVar(&cfg.TLSCert, "tls-cert", "", "PEM file of the server certificate")
	fs.StringVar(&cfg.TLSKey, "tls-key", "", "PEM file of the server certificate's key")
	fs.StringVar(&cfg.BootstrapTokenFile, "bootstrap-token-file", "", "file of valid bootstrap tokens, one per line; needs the tls flags")
	fs.StringVar(&cfg.AuthorizationMode, "authorization-mode", ModeAlwaysAllow, "AlwaysAllow, or RBAC: the Node authorizer, then cluster roles and bindings; RBAC needs the tls flags")
	fs.DurationVar(&cfg.DialTimeout, "etcd-dial-timeout", 5*time.Second, "time allowed to reach etcd at start")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	for _, e := range strings.Split(*endpoints, ",") {
		if e = strings.TrimSpace(e); e != "" {
			cfg.Endpoints = append(cfg.Endpoints, e)
		}
	}
	if len(cfg.Endpoints) == 0 {
		return Config{}, errors.New("no etcd endpoint given")
	}
	if cfg.DialTimeout <= 0 {
		return Config{}, fmt.Errorf("etcd dial timeout %s is not positive", cfg.DialTimeout)
	}
	if err := cfg.tokenFileNeedsTLS(); err != nil {
		return Config{}, err
	}
	if err := cfg.validateAuthorization(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

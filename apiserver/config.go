package apiserver

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
)

// Config is how gaon-apiserver is run.
type Config struct {
	// Listen is the address the API is served on. Anything but a loopback
	// address needs the TLS files.
	Listen string
	// Endpoints are the etcd client endpoints of the cell's Store.
	Endpoints []string
	// EtcdCA, EtcdCert and EtcdKey are the paths of the CA that signs etcd's
	// certificate and of the client certificate and key presented to etcd. All
	// three are set or none is; with none, etcd is dialled without TLS
	// configuration.
	EtcdCA, EtcdCert, EtcdKey string
	// DialTimeout bounds the connection to etcd at start.
	DialTimeout time.Duration
	// EtcdKeepAliveTime and EtcdKeepAliveTimeout bound how long a request to an
	// etcd member that stopped answering waits before it fails and the client
	// moves to another member. Zero EtcdKeepAliveTime turns the ping off.
	EtcdKeepAliveTime, EtcdKeepAliveTimeout time.Duration
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
	// ServiceAccountIssuer is the "iss" of the service account tokens, and
	// ServiceAccountSigningKeyFile the PEM file of the EC P-256 key that signs
	// them. Both are set or neither is; with neither, the token request is
	// refused and no token is authenticated. The issuer is an https URL
	// without a path, query or fragment.
	ServiceAccountIssuer, ServiceAccountSigningKeyFile string
	// ServiceAccountVerifyKeyFiles are PEM files of public keys that the
	// server publishes and accepts beside the signing key's, for a rotation.
	// They need the issuer and the signing key.
	ServiceAccountVerifyKeyFiles []string
	// APIAudiences are the audiences the server accepts in a service account
	// token: a token is authenticated only if its aud holds one of them. Where
	// none is given the issuer is the one. They need the issuer.
	APIAudiences []string
	// ServiceClusterIPRange is the CIDR Service addresses are allocated from,
	// the service prefix, and LoadBalancerPool the CIDR of LAN addresses
	// LoadBalancer Services take theirs from. A range left empty allocates
	// nothing, and a Service that needs an address from it is refused. They do
	// not overlap, and every API server of a cell is given the same two.
	ServiceClusterIPRange, LoadBalancerPool string
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
	fs.StringVar(&cfg.ServiceAccountIssuer, "service-account-issuer", "", "the iss of service account tokens; given with service-account-signing-key-file")
	fs.StringVar(&cfg.ServiceAccountSigningKeyFile, "service-account-signing-key-file", "", "PEM file of the EC P-256 key that signs service account tokens, the same on every API server; given with service-account-issuer")
	verifyKeys := fs.String("service-account-verify-key-files", "", "comma separated PEM files of public keys published and accepted beside the signing key's, for a rotation; given with service-account-issuer")
	audiences := fs.String("api-audiences", "", "comma separated audiences a service account token must hold one of to authenticate; defaults to service-account-issuer")
	fs.StringVar(&cfg.EtcdCA, "etcd-ca-file", "", "PEM file of the CA that signs etcd's certificate; given with etcd-cert-file and etcd-key-file")
	fs.StringVar(&cfg.EtcdCert, "etcd-cert-file", "", "PEM file of the client certificate presented to etcd; given with etcd-ca-file and etcd-key-file")
	fs.StringVar(&cfg.EtcdKey, "etcd-key-file", "", "PEM file of the client certificate's key; given with etcd-ca-file and etcd-cert-file")
	fs.StringVar(&cfg.ServiceClusterIPRange, "service-cluster-ip-range", "", "CIDR Service cluster IPs are allocated from, a virtual prefix; the same on every API server")
	fs.StringVar(&cfg.LoadBalancerPool, "load-balancer-pool", "", "CIDR of LAN addresses LoadBalancer Services are allocated from; the same on every API server, and not overlapping service-cluster-ip-range")
	fs.DurationVar(&cfg.DialTimeout, "etcd-dial-timeout", 5*time.Second, "time allowed to reach etcd at start")
	fs.DurationVar(&cfg.EtcdKeepAliveTime, "etcd-keepalive-time", 10*time.Second, "silence on an etcd connection with a request in flight before the client pings it; 0 sends no ping. The members' --grpc-keepalive-min-time (default 5s) is its lower bound")
	fs.DurationVar(&cfg.EtcdKeepAliveTimeout, "etcd-keepalive-timeout", 5*time.Second, "time to wait for the answer to a ping before the connection is dropped and another member is used")
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
	for _, f := range strings.Split(*verifyKeys, ",") {
		if f = strings.TrimSpace(f); f != "" {
			cfg.ServiceAccountVerifyKeyFiles = append(cfg.ServiceAccountVerifyKeyFiles, f)
		}
	}
	for _, a := range strings.Split(*audiences, ",") {
		if a = strings.TrimSpace(a); a != "" {
			cfg.APIAudiences = append(cfg.APIAudiences, a)
		}
	}
	if cfg.DialTimeout <= 0 {
		return Config{}, fmt.Errorf("etcd dial timeout %s is not positive", cfg.DialTimeout)
	}
	if cfg.EtcdKeepAliveTime < 0 || cfg.EtcdKeepAliveTimeout < 0 || (cfg.EtcdKeepAliveTime > 0 && cfg.EtcdKeepAliveTimeout == 0) {
		return Config{}, fmt.Errorf("etcd keepalive time %s and timeout %s must not be negative, and a time needs a timeout", cfg.EtcdKeepAliveTime, cfg.EtcdKeepAliveTimeout)
	}
	if err := cfg.validateEtcdTLS(); err != nil {
		return Config{}, err
	}
	if err := cfg.tokenFileNeedsTLS(); err != nil {
		return Config{}, err
	}
	if err := cfg.validateAuthorization(); err != nil {
		return Config{}, err
	}
	if err := cfg.validateTokenSigning(); err != nil {
		return Config{}, err
	}
	if _, err := cfg.ranges(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateTokenSigning refuses an issuer without a key and a key without an
// issuer, because a token needs both, verification keys without them, and an
// issuer that is not an https URL on the server's root, because the discovery
// document is served at the issuer's well-known path.
func (c Config) validateTokenSigning() error {
	if (c.ServiceAccountIssuer == "") != (c.ServiceAccountSigningKeyFile == "") {
		return errors.New("service-account-issuer and service-account-signing-key-file are given together or not at all")
	}
	if c.ServiceAccountIssuer == "" {
		if len(c.ServiceAccountVerifyKeyFiles) > 0 {
			return errors.New("service-account-verify-key-files needs service-account-issuer and service-account-signing-key-file")
		}
		if len(c.APIAudiences) > 0 {
			return errors.New("api-audiences needs service-account-issuer and service-account-signing-key-file")
		}
		return nil
	}
	u, err := url.Parse(c.ServiceAccountIssuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("service-account-issuer %q is not an https URL of a host, with no path, query or fragment", c.ServiceAccountIssuer)
	}
	return nil
}

// ranges parses the two address ranges. A range is a CIDR in canonical form,
// with no host bits, of global unicast addresses, and the two do not overlap,
// because an address of both would be held by a service and a LAN host at once.
func (c Config) ranges() (resourceapi.Ranges, error) {
	var r resourceapi.Ranges
	var err error
	if r.ClusterIP, err = parseRange("service-cluster-ip-range", c.ServiceClusterIPRange); err != nil {
		return resourceapi.Ranges{}, err
	}
	if r.LoadBalancer, err = parseRange("load-balancer-pool", c.LoadBalancerPool); err != nil {
		return resourceapi.Ranges{}, err
	}
	if r.ClusterIP.IsValid() && r.LoadBalancer.IsValid() && r.ClusterIP.Overlaps(r.LoadBalancer) {
		return resourceapi.Ranges{}, fmt.Errorf("service-cluster-ip-range %s and load-balancer-pool %s overlap", r.ClusterIP, r.LoadBalancer)
	}
	return r, nil
}

func parseRange(flag, text string) (netip.Prefix, error) {
	if text == "" {
		return netip.Prefix{}, nil
	}
	p, err := netip.ParsePrefix(text)
	if err != nil || p.Addr().Zone() != "" || p.Masked() != p {
		return netip.Prefix{}, fmt.Errorf("%s %q is not a CIDR in canonical form with no host bits", flag, text)
	}
	if !p.Addr().IsGlobalUnicast() {
		return netip.Prefix{}, fmt.Errorf("%s %q does not hold global unicast addresses", flag, text)
	}
	return p, nil
}

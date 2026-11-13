package proxy_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

const gwName = "edge"

// logBuffer is a log destination that can be read while it is written.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is a Proxy whose ports open on loopback addresses the test can find.
type harness struct {
	*proxy.Proxy
	logs *logBuffer

	mu  sync.Mutex
	lns map[int32]net.Listener
}

func newHarness(t *testing.T, cfg proxy.Config) *harness {
	t.Helper()
	h := &harness{logs: &logBuffer{}, lns: map[int32]net.Listener{}}
	cfg.Gateway = gwName
	cfg.Listen = func(port int32) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.lns[port] = ln
		h.mu.Unlock()
		return ln, nil
	}
	h.Proxy = proxy.New(cfg, slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(h.Proxy.Close)
	return h
}

// addr returns the address the listener of the port accepts on.
func (h *harness) addr(t *testing.T, port int32) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	ln, ok := h.lns[port]
	require.True(t, ok, "port %d is not open", port)
	return ln.Addr().String()
}

func (h *harness) opened(port int32) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.lns[port]
	return ok
}

func customObject(t *testing.T, kind, name string, spec, status any) api.CustomObject {
	t.Helper()
	o := api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: gateway.APIVersion, Kind: kind},
		Metadata: api.ObjectMeta{Name: name},
	}
	var err error
	o.Spec, err = json.Marshal(spec)
	require.NoError(t, err)
	if status != nil {
		o.Status, err = json.Marshal(status)
		require.NoError(t, err)
	}
	return o
}

func gatewayOf(t *testing.T, listeners ...gateway.Listener) api.CustomObject {
	t.Helper()
	return customObject(t, gateway.KindGateway, gwName, gateway.GatewaySpec{Listeners: listeners}, nil)
}

func httpListener(name string, port int32, hostname string) gateway.Listener {
	return gateway.Listener{Name: name, Port: port, Protocol: gateway.ProtocolHTTP, Hostname: hostname}
}

func httpsListener(name string, port int32, hostname, secret string) gateway.Listener {
	return gateway.Listener{Name: name, Port: port, Protocol: gateway.ProtocolHTTPS, Hostname: hostname,
		TLS: &gateway.ListenerTLS{Mode: gateway.ModeTerminate, CertificateRefs: []gateway.SecretRef{{Name: secret}}}}
}

func tlsListener(name string, port int32, hostname string) gateway.Listener {
	return gateway.Listener{Name: name, Port: port, Protocol: gateway.ProtocolTLS, Hostname: hostname,
		TLS: &gateway.ListenerTLS{Mode: gateway.ModePassthrough}}
}

// ruleTo is a rule with the path prefixes, an optional replacement of the
// prefix, and the Service port 80 of backend.
type ruleTo struct {
	prefixes []string
	rewrite  *string
	backend  string
}

func rule(t *testing.T, r ruleTo) gateway.Rule {
	t.Helper()
	out := gateway.Rule{BackendRefs: []gateway.BackendRef{{Name: r.backend, Port: 80}}}
	for _, p := range r.prefixes {
		raw, err := json.Marshal(map[string]any{"path": map[string]string{"type": "PathPrefix", "value": p}})
		require.NoError(t, err)
		out.Matches = append(out.Matches, raw)
	}
	if r.rewrite != nil {
		raw, err := json.Marshal(map[string]any{"type": "URLRewrite", "urlRewrite": map[string]any{
			"path": map[string]string{"type": "ReplacePrefixMatch", "replacePrefixMatch": *r.rewrite}}})
		require.NoError(t, err)
		out.Filters = append(out.Filters, raw)
	}
	return out
}

func acceptedStatus(accepted bool) gateway.RouteStatus {
	status := api.ConditionTrue
	if !accepted {
		status = api.ConditionFalse
	}
	return gateway.RouteStatus{Parents: []gateway.ParentStatus{{
		ParentRef:  gateway.ParentRef{Name: gwName},
		Conditions: []api.Condition{{Type: gateway.CondAccepted, Status: status}},
	}}}
}

// httpRoute is an HTTPRoute of the Gateway whose parent reference the
// controller recorded as Accepted, or as not Accepted.
func httpRoute(t *testing.T, name string, hostnames []string, accepted bool, rules ...gateway.Rule) api.CustomObject {
	t.Helper()
	spec := gateway.RouteSpec{ParentRefs: []gateway.ParentRef{{Name: gwName}}, Hostnames: hostnames, Rules: rules}
	return customObject(t, gateway.KindHTTPRoute, name, spec, acceptedStatus(accepted))
}

// routeOn is an HTTPRoute whose parent reference names the listener, recorded
// as Accepted.
func routeOn(t *testing.T, name, section string, rules ...gateway.Rule) api.CustomObject {
	t.Helper()
	ref := gateway.ParentRef{Name: gwName, SectionName: section}
	return customObject(t, gateway.KindHTTPRoute, name,
		gateway.RouteSpec{ParentRefs: []gateway.ParentRef{ref}, Rules: rules},
		gateway.RouteStatus{Parents: []gateway.ParentStatus{{
			ParentRef:  ref,
			Conditions: []api.Condition{{Type: gateway.CondAccepted, Status: api.ConditionTrue}},
		}}})
}

func tlsRoute(t *testing.T, name string, hostnames []string, backend string) api.CustomObject {
	t.Helper()
	spec := gateway.RouteSpec{
		ParentRefs: []gateway.ParentRef{{Name: gwName}},
		Hostnames:  hostnames,
		Rules:      []gateway.Rule{{BackendRefs: []gateway.BackendRef{{Name: backend, Port: 80}}}},
	}
	return customObject(t, gateway.KindTLSRoute, name, spec, acceptedStatus(true))
}

// backendOf returns the Service port 80 named name and its Endpoints, with the
// addresses the strings name as host:port. No address makes a Service with no
// ready address.
func backendOf(t *testing.T, name string, addrs ...string) (api.Service, api.Endpoints) {
	t.Helper()
	svc := api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.ServiceSpec{Ports: []api.ServicePort{{Name: "web", Protocol: api.ProtocolTCP, Port: 80}}},
	}
	eps := api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: name},
	}
	byPort := map[int32]*api.EndpointSubset{}
	for _, a := range addrs {
		host, port, err := net.SplitHostPort(a)
		require.NoError(t, err)
		n, err := strconv.Atoi(port)
		require.NoError(t, err)
		s := byPort[int32(n)]
		if s == nil {
			s = &api.EndpointSubset{Ports: []api.EndpointPort{{Name: "web", Port: int32(n), Protocol: api.ProtocolTCP}}}
			byPort[int32(n)] = s
		}
		s.Addresses = append(s.Addresses, api.EndpointAddress{IP: host})
	}
	for _, s := range byPort {
		eps.Subsets = append(eps.Subsets, *s)
	}
	return svc, eps
}

// objects collects the Services and Endpoints of backends, by name and address.
func objects(t *testing.T, gw api.CustomObject, routes []api.CustomObject, backends map[string][]string) proxy.Objects {
	t.Helper()
	o := proxy.Objects{Gateways: []api.CustomObject{gw}}
	for _, r := range routes {
		if r.Kind == gateway.KindTLSRoute {
			o.TLSRoutes = append(o.TLSRoutes, r)
		} else {
			o.HTTPRoutes = append(o.HTTPRoutes, r)
		}
	}
	for name, addrs := range backends {
		svc, eps := backendOf(t, name, addrs...)
		o.Services = append(o.Services, svc)
		o.Endpoints = append(o.Endpoints, eps)
	}
	return o
}

// hostPort returns the host:port of the test server.
func hostPort(t *testing.T, s *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(s.URL)
	require.NoError(t, err)
	return u.Host
}

// serving starts a backend that answers every request with body.
func serving(t *testing.T, body string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(s.Close)
	return hostPort(t, s)
}

// keyPair is a certificate for the names and its private key, in PEM, made for
// the test and used nowhere else.
type keyPair struct {
	crt, key []byte
	cert     *x509.Certificate
}

func newKeyPair(t *testing.T, names ...string) keyPair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gaon test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return keyPair{
		crt:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		cert: cert,
	}
}

func (k keyPair) secret(name string) api.Secret {
	return secretOf(name, k.crt, k.key)
}

func secretOf(name string, crt, key []byte) api.Secret {
	return api.Secret{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecret},
		Metadata: api.ObjectMeta{Name: name},
		Data:     map[string][]byte{"tls.crt": crt, "tls.key": key},
	}
}

func (k keyPair) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(k.cert)
	return p
}

// tlsClient returns an HTTP client that connects to addr whatever the URL
// names, and trusts the pool.
func tlsClient(addr string, pool *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			},
			DisableKeepAlives: true,
		},
	}
}

package api_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// seenRequest is what a tokenServer saw of one request.
type seenRequest struct {
	authorization string
	certificates  int
}

// tokenServer answers every request with a Node and records what it saw of the
// caller. It serves TLS signed by ca and asks for client certificates as
// clientAuth says.
type tokenServer struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newTokenServer(t *testing.T, ca *testCA, clientAuth tls.ClientAuthType) *tokenServer {
	t.Helper()
	s := &tokenServer{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, seenRequest{r.Header.Get("Authorization"), len(r.TLS.PeerCertificates)})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(newNode("node-a"))
	}))
	certPEM, keyPEM := ca.issue(t, "server")
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	clients := x509.NewCertPool()
	clients.AddCert(ca.cert)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: clientAuth, ClientCAs: clients}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func (s *tokenServer) requests() []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seenRequest(nil), s.seen...)
}

func TestWithBearerTokenSendsTheTokenOverTLSVerifiedAgainstTheCA(t *testing.T) {
	ca := newTestCA(t, "ca")
	other := newTestCA(t, "other")
	ts := newTokenServer(t, ca, tls.VerifyClientCertIfGiven)
	ctx := testContext(t)

	trusted := writeFiles(t, ca.pem, nil, nil)
	httpClient, err := api.NewServerTLSClient(trusted.ca)
	require.NoError(t, err)
	_, err = api.NewClient(ts.URL, httpClient, api.WithBearerToken("abc123.0123456789abcdef")).GetNode(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, []seenRequest{{"Bearer abc123.0123456789abcdef", 0}}, ts.requests(), "the token arrives and no certificate does")

	untrusted := writeFiles(t, other.pem, nil, nil)
	wrongCA, err := api.NewServerTLSClient(untrusted.ca)
	require.NoError(t, err)
	_, err = api.NewClient(ts.URL, wrongCA, api.WithBearerToken("abc123.0123456789abcdef")).GetNode(ctx, "node-a")
	assert.Error(t, err)
	assert.Len(t, ts.requests(), 1, "a server the CA file does not vouch for is sent no token")
}

func TestWithBearerTokenIsNotSentOverPlainHTTP(t *testing.T) {
	var mu sync.Mutex
	var requests int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(newNode("node-a"))
	}))
	t.Cleanup(plain.Close)
	ctx := testContext(t)

	_, err := api.NewClient(plain.URL, plain.Client()).GetNode(ctx, "node-a")
	require.NoError(t, err, "control: without a token the plain request goes out")

	_, err = api.NewClient(plain.URL, plain.Client(), api.WithBearerToken("abc123.0123456789abcdef")).GetNode(ctx, "node-a")
	assert.Error(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, requests, "the request with a token is not sent")
}

func TestWithBearerTokenLeavesTheMutualTLSClientAsItWas(t *testing.T) {
	ca := newTestCA(t, "ca")
	ts := newTokenServer(t, ca, tls.RequireAndVerifyClientCert)
	ctx := testContext(t)
	f := clientFiles(t, ca, ca)

	withCert, err := api.NewTLSClient(f.ca, f.cert, f.key)
	require.NoError(t, err)
	_, err = api.NewClient(ts.URL, withCert, api.WithBearerToken("abc123.0123456789abcdef")).GetNode(ctx, "node-a")
	require.NoError(t, err, "control: a client with a certificate is accepted with a token too")
	assert.Equal(t, []seenRequest{{"Bearer abc123.0123456789abcdef", 1}}, ts.requests(), "the certificate is still presented")

	noCert, err := api.NewServerTLSClient(f.ca)
	require.NoError(t, err)
	_, err = api.NewClient(ts.URL, noCert, api.WithBearerToken("abc123.0123456789abcdef")).GetNode(ctx, "node-a")
	assert.Error(t, err, "a token in place of the certificate does not pass a server that requires one")
	assert.Len(t, ts.requests(), 1)
}

func TestNewServerTLSClientRefusesTLS12AndAFileItCannotUse(t *testing.T) {
	ca := newTestCA(t, "ca")
	f := writeFiles(t, ca.pem, nil, nil)

	c, err := api.NewServerTLSClient(f.ca)
	require.NoError(t, err)
	current := newTokenServer(t, ca, tls.NoClientCert)
	_, err = api.NewClient(current.URL, c).GetNode(testContext(t), "node-a")
	require.NoError(t, err, "control: a TLS 1.3 server is reached")

	old := httptest.NewUnstartedServer(http.NotFoundHandler())
	certPEM, keyPEM := ca.issue(t, "server")
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	old.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MaxVersion: tls.VersionTLS12}
	old.StartTLS()
	t.Cleanup(old.Close)
	_, err = api.NewClient(old.URL, c).GetNode(testContext(t), "node-a")
	assert.Error(t, err)

	noCert := writeFiles(t, []byte("not a certificate\n"), nil, nil)
	_, err = api.NewServerTLSClient(noCert.ca)
	assert.ErrorContains(t, err, noCert.ca)
	_, err = api.NewServerTLSClient(f.ca + ".absent")
	assert.ErrorContains(t, err, f.ca+".absent")
}

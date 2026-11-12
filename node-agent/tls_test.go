package nodeagent_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	nodeagent "github.com/garamsh/gaon/node-agent"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
)

// mtlsAPI is an API server that requires client certificates, and the paths of
// a CA, certificate and key it accepts.
type mtlsAPI struct {
	URL                 string
	ca, cert, key       string
	requests, withCerts atomic.Int32
}

// newMTLSAPI starts a TLS server that verifies client certificates against a CA
// generated here and answers an empty MicroVM list.
func newMTLSAPI(t *testing.T) *mtlsAPI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	issue := func(cn string) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage:    x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		}, caCert, &key.PublicKey, caKey)
		require.NoError(t, err)
		keyDER, err := x509.MarshalECPrivateKey(key)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	}

	srvCert, srvKey := issue("server")
	pair, err := tls.X509KeyPair(srvCert, srvKey)
	require.NoError(t, err)
	clients := x509.NewCertPool()
	clients.AddCert(caCert)
	a := &mtlsAPI{}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests.Add(1)
		if len(r.TLS.PeerCertificates) > 0 {
			a.withCerts.Add(1)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"MicroVMList","metadata":{"resourceVersion":"1"},"items":[]}`)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients}
	ts.StartTLS()
	t.Cleanup(ts.Close)

	cliCert, cliKey := issue("client")
	dir := t.TempDir()
	a.URL = ts.URL
	a.ca, a.cert, a.key = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(a.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))
	require.NoError(t, os.WriteFile(a.cert, cliCert, 0o600))
	require.NoError(t, os.WriteFile(a.key, cliKey, 0o600))
	return a
}

func (a *mtlsAPI) flags() []string {
	return []string{"--tls-ca", a.ca, "--tls-cert", a.cert, "--tls-key", a.key}
}

func TestParseConfig_ReachesTheAPIServerOverMutualTLSWhenGivenTheThreeFiles(t *testing.T) {
	a := newMTLSAPI(t)
	args := append([]string{"--api-server", a.URL}, a.flags()...)

	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)

	_, err = api.NewClient(a.URL, cfg.HTTPClient).ListMicroVMs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), a.requests.Load())
	assert.Equal(t, int32(1), a.withCerts.Load(), "the server saw the client certificate")
}

func TestParseConfig_TakesTheTLSFilesFromTheEnvironmentAndAFlagWins(t *testing.T) {
	a := newMTLSAPI(t)
	environment := map[string]string{
		nodeagent.EnvAPIServer: a.URL, nodeagent.EnvNodeName: "n1", nodeagent.EnvMvmdSocket: "/run/mvmd.sock",
		nodeagent.EnvTLSCA: a.ca, nodeagent.EnvTLSCert: a.cert, nodeagent.EnvTLSKey: a.key,
	}

	cfg, err := nodeagent.ParseConfig(nil, env(environment), io.Discard)
	require.NoError(t, err)
	_, err = api.NewClient(a.URL, cfg.HTTPClient).ListMicroVMs(context.Background())
	require.NoError(t, err, "the three variables alone build a mutual TLS client")

	_, err = nodeagent.ParseConfig([]string{"--tls-ca", filepath.Join(t.TempDir(), "missing.pem")}, env(environment), io.Discard)
	assert.ErrorContains(t, err, "missing.pem", "a flag given wins over its variable")
}

func TestParseConfig_KeepsPlainHTTPWithNoTLSFlag(t *testing.T) {
	cfg, err := nodeagent.ParseConfig(nil, env(required), io.Discard)
	require.NoError(t, err)

	require.NotNil(t, cfg.HTTPClient)
	assert.Nil(t, cfg.HTTPClient.Transport, "no TLS identity, so the default transport")
}

func TestRun_CallsTheAPIServerWithTheClientFromItsConfig(t *testing.T) {
	a := newMTLSAPI(t)
	clk := clock.System{}
	mvmd := fakemvmd.New(t, clk)
	args := append([]string{"--api-server", a.URL, "--mvmd-socket", mvmd.Socket}, a.flags()...)
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- nodeagent.Run(ctx, cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() { cancel(); <-done })

	require.Eventually(t, func() bool { return a.withCerts.Load() > 0 }, 5*time.Second, 10*time.Millisecond,
		"the agent's calls reach the server with the client certificate")
	assert.Equal(t, a.requests.Load(), a.withCerts.Load(), "every call that got through carried it")
}

func TestParseConfig_RefusesAPartialTLSSet(t *testing.T) {
	a := newMTLSAPI(t)
	tests := []struct {
		name    string
		args    []string
		environ map[string]string
		missing string
	}{
		{"ca only", []string{"--tls-ca", a.ca}, nil, "--tls-cert, --tls-key"},
		{"cert and key", []string{"--tls-cert", a.cert, "--tls-key", a.key}, nil, "--tls-ca"},
		{"key from the environment only", nil, map[string]string{nodeagent.EnvTLSKey: a.key}, "--tls-ca, --tls-cert"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			environment := map[string]string{}
			for k, v := range required {
				environment[k] = v
			}
			for k, v := range tt.environ {
				environment[k] = v
			}

			_, err := nodeagent.ParseConfig(tt.args, env(environment), io.Discard)

			assert.ErrorContains(t, err, tt.missing)
		})
	}
	_, err := nodeagent.ParseConfig(a.flags(), env(required), io.Discard)
	assert.NoError(t, err, "control: the three together are accepted")
}

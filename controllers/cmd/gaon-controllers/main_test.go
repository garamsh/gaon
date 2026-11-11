package main

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

	"github.com/garamsh/gaon/controllers/internal/nodelifecycle"
	"github.com/garamsh/gaon/lib/api"
)

// mtlsAPI is an API server that requires client certificates, and the paths of
// a CA, certificate and key it accepts.
type mtlsAPI struct {
	URL                 string
	ca, cert, key       string
	requests, withCerts atomic.Int32
}

// newMTLSAPI starts a TLS server that verifies client certificates against a CA
// generated here and answers an empty Machine list.
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
		_, _ = io.WriteString(w, `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"MachineList","metadata":{"resourceVersion":"1"},"items":[]}`)
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
	return []string{"-tls-ca", a.ca, "-tls-cert", a.cert, "-tls-key", a.key}
}

func TestParseFlags_ReachesTheAPIOverMutualTLSWhenGivenTheThreeFiles(t *testing.T) {
	a := newMTLSAPI(t)

	cfg, err := parseFlags(append([]string{"-api", a.URL}, a.flags()...), io.Discard)
	require.NoError(t, err)

	_, err = api.NewClient(cfg.root, cfg.httpClient).ListMachines(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), a.requests.Load())
	assert.Equal(t, int32(1), a.withCerts.Load(), "the server saw the client certificate")
	assert.Zero(t, cfg.httpClient.Timeout, "a watch is one long response")
}

func TestParseFlags_KeepsPlainHTTPWithNoTLSFlag(t *testing.T) {
	cfg, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-lease-margin", "2s", "-default-lease-duration", "9s"}, io.Discard)
	require.NoError(t, err)

	assert.Equal(t, "http://127.0.0.1:1", cfg.root)
	assert.Equal(t, nodelifecycle.Config{Margin: 2 * time.Second, DefaultLeaseDuration: 9 * time.Second}, cfg.lifecycle)
	assert.Nil(t, cfg.httpClient.Transport, "no TLS identity, so the default transport")
}

func TestParseFlags_RefusesAPartialTLSSet(t *testing.T) {
	a := newMTLSAPI(t)
	tests := []struct {
		name    string
		tls     []string
		missing string
	}{
		{"ca only", []string{"-tls-ca", a.ca}, "--tls-cert, --tls-key"},
		{"cert and key", []string{"-tls-cert", a.cert, "-tls-key", a.key}, "--tls-ca"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFlags(append([]string{"-api", a.URL}, tt.tls...), io.Discard)

			assert.ErrorContains(t, err, tt.missing)
		})
	}
	_, err := parseFlags(append([]string{"-api", a.URL}, a.flags()...), io.Discard)
	assert.NoError(t, err, "control: the three together are accepted")
}

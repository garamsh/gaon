package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/garbagecollector"
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
	return []string{"-tls-ca", a.ca, "-tls-cert", a.cert, "-tls-key", a.key}
}

func TestParseFlags_ReachesTheAPIOverMutualTLSWhenGivenTheThreeFiles(t *testing.T) {
	a := newMTLSAPI(t)

	cfg, err := parseFlags(append([]string{"-api", a.URL}, a.flags()...), io.Discard)
	require.NoError(t, err)

	_, err = api.NewClient(cfg.root, cfg.httpClient).ListMicroVMs(context.Background())
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

// writeCA writes a self-signed CA certificate and its key as PEM files and
// returns their paths.
func writeCA(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}

func TestParseFlags_RunsTheSignerOnlyWithTheSigningFiles(t *testing.T) {
	certFile, keyFile := writeCA(t)

	without, err := parseFlags([]string{"-api", "http://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)
	with, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-signing-cert", certFile, "-signing-key", keyFile}, io.Discard)
	require.NoError(t, err)

	assert.Nil(t, without.signer, "no signing files, so the signer does not run")
	require.NotNil(t, with.signer)
	assert.Equal(t, 8760*time.Hour, with.signer.Duration, "the default signing duration")
}

func TestParseFlags_TakesTheSigningDuration(t *testing.T) {
	certFile, keyFile := writeCA(t)

	cfg, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-signing-cert", certFile, "-signing-key", keyFile, "-signing-duration", "36h"}, io.Discard)

	require.NoError(t, err)
	assert.Equal(t, 36*time.Hour, cfg.signer.Duration)
}

func TestParseFlags_RefusesAPartialSigningSet(t *testing.T) {
	certFile, keyFile := writeCA(t)
	tests := []struct {
		name string
		args []string
	}{
		{"cert only", []string{"-signing-cert", certFile}},
		{"key only", []string{"-signing-key", keyFile}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFlags(append([]string{"-api", "http://127.0.0.1:1"}, tt.args...), io.Discard)

			assert.ErrorContains(t, err, "-signing-cert and -signing-key")
		})
	}
	_, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-signing-cert", certFile, "-signing-key", keyFile}, io.Discard)
	assert.NoError(t, err, "control: the two together are accepted")
}

func TestParseFlags_RefusesSigningFilesThatDoNotLoadOrDoNotMatch(t *testing.T) {
	certFile, keyFile := writeCA(t)
	_, otherKey := writeCA(t)
	missing := filepath.Join(t.TempDir(), "none.pem")
	tests := []struct {
		name      string
		cert, key string
	}{
		{"missing cert", missing, keyFile},
		{"missing key", certFile, missing},
		{"key of another CA", certFile, otherKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-signing-cert", tt.cert, "-signing-key", tt.key}, io.Discard)

			assert.Error(t, err)
		})
	}
}

func TestParseFlags_RefusesASigningDurationAtOrBelowZero(t *testing.T) {
	certFile, keyFile := writeCA(t)
	for _, d := range []string{"0s", "-1h"} {
		t.Run(d, func(t *testing.T) {
			_, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-signing-cert", certFile, "-signing-key", keyFile, "-signing-duration", d}, io.Discard)

			assert.ErrorContains(t, err, "-signing-duration")
		})
	}
}

func TestRunAll_AFailureEndsTheOthersAndIsReported(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	waits := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	fails := func(context.Context) error { return errors.New("down") }

	assert.False(t, runAll(context.Background(), log, map[string]func(context.Context) error{"waits": waits, "fails": fails}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.True(t, runAll(ctx, log, map[string]func(context.Context) error{"waits": waits}), "control: ending by cancellation is not a failure")
}

func TestParseFlags_TakesTheServerFromTheComputeRootAndRunsTheCollectorOnlyThen(t *testing.T) {
	root := "http://127.0.0.1:1/apis/" + api.GroupVersion

	withSuffix, err := parseFlags([]string{"-api", root + "/", "-gc-interval", "7s"}, io.Discard)
	require.NoError(t, err)
	without, err := parseFlags([]string{"-api", "http://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)

	assert.Equal(t, "http://127.0.0.1:1", withSuffix.server)
	assert.Equal(t, garbagecollector.Config{Interval: 7 * time.Second}, withSuffix.collector)
	assert.Empty(t, without.server, "no compute suffix, so no other group's root can be derived and the collector does not run")
	assert.Equal(t, 30*time.Second, without.collector.Interval, "the default interval")
}

func TestParseFlags_RefusesAGCIntervalAtOrBelowZero(t *testing.T) {
	for _, d := range []string{"0s", "-1m"} {
		t.Run(d, func(t *testing.T) {
			_, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-gc-interval", d}, io.Discard)

			assert.ErrorContains(t, err, "-gc-interval")
		})
	}
}

// ownedVolumeAPI serves one Volume whose only owner is a MicroVM that is not
// stored, on the paths the collector reads, and records the Volume's delete.
func ownedVolumeAPI(t *testing.T) (url string, deleted <-chan string) {
	t.Helper()
	got := make(chan string, 1)
	var mu sync.Mutex
	volume := api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
		Metadata: api.ObjectMeta{Name: "data", UID: "uid-v", ResourceVersion: "5", OwnerReferences: []api.OwnerReference{
			{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "gone", UID: "uid-m"},
		}},
	}
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		require.NoError(t, json.NewEncoder(w).Encode(v))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apis/"+api.GroupVersion+"/volumes", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, api.CustomObjectList{Items: []api.CustomObject{volume}})
	})
	mux.HandleFunc("DELETE /apis/"+api.GroupVersion+"/volumes/data", func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Query().Get("resourceVersion")
		writeJSON(w, http.StatusOK, volume)
	})
	mux.HandleFunc("GET /apis/{group}/{version}/{plural}/{name}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "object"))
	})
	mux.HandleFunc("GET /apis/{group}/{version}/{plural}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CustomObjectList{Items: []api.CustomObject{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, got
}

func TestNewCollector_DeletesAnObjectWhoseOwnerIsGoneThroughTheShippedWiring(t *testing.T) {
	url, deleted := ownedVolumeAPI(t)
	cfg, err := parseFlags([]string{"-api", url + "/apis/" + api.GroupVersion}, io.Discard)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(cfg.server, url), "setup: the collector is pointed at the test server")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newCollector(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})

	select {
	case rv := <-deleted:
		assert.Equal(t, "5", rv, "deleted at the version it listed")
	case <-time.After(5 * time.Second):
		t.Fatal("the collector did not delete the Volume")
	}
}

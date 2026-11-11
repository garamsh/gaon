package apiserver_test

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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
)

// authority is a CA made in this process, and what it signs.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newAuthority(t *testing.T, name string) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &authority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue signs a certificate for cn valid for the server and client roles, and
// returns it with its key, both PEM.
func (a *authority) issue(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// tlsServer is the API server on a wildcard address with mutual TLS, trusting
// the clients of one CA.
type tlsServer struct {
	addr     string
	serverCA *authority
	clientCA *authority
}

func newTLSServer(t *testing.T) *tlsServer {
	t.Helper()
	s := &tlsServer{serverCA: newAuthority(t, "server ca"), clientCA: newAuthority(t, "client ca")}
	certPEM, keyPEM := s.serverCA.issue(t, "gaon-apiserver")
	cfg := apiserver.Config{
		Endpoints:   []string{startEtcd(t)},
		DialTimeout: 5 * time.Second,
		TLSClientCA: writeFile(t, "client-ca.pem", s.clientCA.pem),
		TLSCert:     writeFile(t, "server.pem", certPEM),
		TLSKey:      writeFile(t, "server-key.pem", keyPEM),
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- apiserver.Run(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	s.addr = net.JoinHostPort("127.0.0.1", port)
	return s
}

// get lists Machines as a caller presenting cert, or none where cert is nil,
// speaking TLS up to maxVersion where it is not zero, and returns the status.
func (s *tlsServer) get(t *testing.T, cert *tls.Certificate, maxVersion uint16) (int, error) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(s.serverCA.cert)
	cfg := &tls.Config{RootCAs: roots, MaxVersion: maxVersion}
	if cert != nil {
		// Send it whatever the server's CA list says, so the server decides.
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + s.addr + apiRoot + "/machines")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, err
}

func clientCert(t *testing.T, a *authority, cn string) *tls.Certificate {
	t.Helper()
	certPEM, keyPEM := a.issue(t, cn)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return &cert
}

func TestRun_ServesACallerWithACertificateTheClientCASigned(t *testing.T) {
	s := newTLSServer(t)

	status, err := s.get(t, clientCert(t, s.clientCA, "node-1"), 0)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
}

func TestRun_RefusesACallerWithoutACertificate(t *testing.T) {
	s := newTLSServer(t)

	_, err := s.get(t, nil, 0)
	status, controlErr := s.get(t, clientCert(t, s.clientCA, "node-1"), 0)

	assert.Error(t, err)
	require.NoError(t, controlErr, "control: the same server serves a caller with a valid certificate")
	assert.Equal(t, http.StatusOK, status)
}

func TestRun_RefusesACallerWithACertificateFromAnotherCA(t *testing.T) {
	s := newTLSServer(t)
	other := newAuthority(t, "other ca")

	_, err := s.get(t, clientCert(t, other, "node-1"), 0)
	status, controlErr := s.get(t, clientCert(t, s.clientCA, "node-1"), 0)

	assert.Error(t, err)
	require.NoError(t, controlErr, "control: the same server serves a caller with a valid certificate")
	assert.Equal(t, http.StatusOK, status)
}

func TestRun_RefusesTLSBelow13(t *testing.T) {
	s := newTLSServer(t)
	cert := clientCert(t, s.clientCA, "node-1")

	_, err := s.get(t, cert, tls.VersionTLS12)
	status, controlErr := s.get(t, cert, 0)

	assert.Error(t, err)
	require.NoError(t, controlErr, "control: the same caller is served over 1.3")
	assert.Equal(t, http.StatusOK, status)
}

func TestRun_RefusesTLSFilesThatDoNotLoad(t *testing.T) {
	ca := newAuthority(t, "ca")
	certPEM, keyPEM := ca.issue(t, "gaon-apiserver")
	good := apiserver.Config{
		Endpoints:   []string{"http://127.0.0.1:1"},
		DialTimeout: time.Second,
		TLSClientCA: writeFile(t, "ca.pem", ca.pem),
		TLSCert:     writeFile(t, "cert.pem", certPEM),
		TLSKey:      writeFile(t, "key.pem", keyPEM),
	}
	for name, tc := range map[string]struct {
		mutate func(*apiserver.Config)
		want   string
	}{
		"missing certificate":      {func(c *apiserver.Config) { c.TLSCert = filepath.Join(t.TempDir(), "none.pem") }, "load server certificate"},
		"CA without a certificate": {func(c *apiserver.Config) { c.TLSClientCA = writeFile(t, "empty.pem", []byte("not pem")) }, "holds no certificate"},
		"partial set":              {func(c *apiserver.Config) { c.TLSKey = "" }, "all together"},
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer ln.Close()
			cfg := good
			tc.mutate(&cfg)

			err = apiserver.Run(context.Background(), cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

			assert.ErrorContains(t, err, tc.want)
		})
	}
}

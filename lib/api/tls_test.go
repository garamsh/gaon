package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// testCA is a certificate authority generated in the test.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) *testCA {
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
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns a leaf certificate and key, both PEM, signed by ca for both
// server and client use.
func (ca *testCA) issue(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// tlsFiles are the three files of a client identity on disk.
type tlsFiles struct{ ca, cert, key string }

func writeFiles(t *testing.T, caPEM, certPEM, keyPEM []byte) tlsFiles {
	t.Helper()
	dir := t.TempDir()
	f := tlsFiles{filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")}
	require.NoError(t, os.WriteFile(f.ca, caPEM, 0o600))
	require.NoError(t, os.WriteFile(f.cert, certPEM, 0o600))
	require.NoError(t, os.WriteFile(f.key, keyPEM, 0o600))
	return f
}

// clientFiles writes a client certificate issued by issuer, trusting trust.
func clientFiles(t *testing.T, trust, issuer *testCA) tlsFiles {
	t.Helper()
	certPEM, keyPEM := issuer.issue(t, "client")
	return writeFiles(t, trust.pem, certPEM, keyPEM)
}

// mtlsServer serves "ok" over TLS with a certificate ca signed, requiring and
// verifying client certificates against ca. maxVersion 0 leaves the default.
func mtlsServer(t *testing.T, ca *testCA, maxVersion uint16) *httptest.Server {
	t.Helper()
	certPEM, keyPEM := ca.issue(t, "server")
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	clients := x509.NewCertPool()
	clients.AddCert(ca.cert)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clients,
		MaxVersion:   maxVersion,
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func get(c *http.Client, url string) (string, error) {
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestNewTLSClient_ServedByAServerRequiringClientCertificates(t *testing.T) {
	ca := newTestCA(t, "ca")
	ts := mtlsServer(t, ca, 0)
	f := clientFiles(t, ca, ca)

	c, err := api.NewTLSClient(f.ca, f.cert, f.key)
	require.NoError(t, err)

	body, err := get(c, ts.URL)
	require.NoError(t, err)
	assert.Equal(t, "ok", body)
}

func TestNewTLSClient_ServerRefusesAClientWithoutACertificate(t *testing.T) {
	ca := newTestCA(t, "ca")
	ts := mtlsServer(t, ca, 0)
	f := clientFiles(t, ca, ca)

	built, err := api.NewTLSClient(f.ca, f.cert, f.key)
	require.NoError(t, err)
	control, err := get(built, ts.URL)
	require.NoError(t, err)
	require.Equal(t, "ok", control)

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	bare := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	_, err = get(bare, ts.URL)
	assert.Error(t, err)
}

func TestNewTLSClient_ServerRefusesACertificateAnotherCASigned(t *testing.T) {
	ca := newTestCA(t, "ca")
	other := newTestCA(t, "other")
	ts := mtlsServer(t, ca, 0)

	good := clientFiles(t, ca, ca)
	goodClient, err := api.NewTLSClient(good.ca, good.cert, good.key)
	require.NoError(t, err)
	control, err := get(goodClient, ts.URL)
	require.NoError(t, err)
	require.Equal(t, "ok", control)

	bad := clientFiles(t, ca, other)
	badClient, err := api.NewTLSClient(bad.ca, bad.cert, bad.key)
	require.NoError(t, err)
	_, err = get(badClient, ts.URL)
	assert.Error(t, err)
}

func TestNewTLSClient_TrustsOnlyTheCAFile(t *testing.T) {
	ca := newTestCA(t, "ca")
	other := newTestCA(t, "other")
	ts := mtlsServer(t, ca, 0)

	// The client certificate is one the server accepts; only the CA file
	// names a root that does not vouch for the server.
	certPEM, keyPEM := ca.issue(t, "client")
	f := writeFiles(t, other.pem, certPEM, keyPEM)
	c, err := api.NewTLSClient(f.ca, f.cert, f.key)
	require.NoError(t, err)
	_, err = get(c, ts.URL)
	assert.Error(t, err)
}

// seenServer is a mutual TLS server answering the CN of the client certificate
// it was given.
func seenServer(t *testing.T, ca *testCA) *httptest.Server {
	t.Helper()
	certPEM, keyPEM := ca.issue(t, "server")
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	clients := x509.NewCertPool()
	clients.AddCert(ca.cert)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cn := "none"
		if len(r.TLS.PeerCertificates) > 0 {
			cn = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		_, _ = io.WriteString(w, cn)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: clients}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func TestNewTLSClientFunc_PresentsTheCertificateTheCallbackReturnsAtEachHandshake(t *testing.T) {
	ca := newTestCA(t, "ca")
	ts := seenServer(t, ca)
	pairFor := func(cn string) *tls.Certificate {
		certPEM, keyPEM := ca.issue(t, cn)
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		require.NoError(t, err)
		return &pair
	}
	var mu sync.Mutex
	current := &tls.Certificate{}
	f := writeFiles(t, ca.pem, nil, nil)
	c, err := api.NewTLSClientFunc(f.ca, func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	})
	require.NoError(t, err)
	reconnect := func() { c.Transport.(*http.Transport).CloseIdleConnections() }
	set := func(p *tls.Certificate) {
		mu.Lock()
		defer mu.Unlock()
		current = p
		reconnect()
	}

	body, err := get(c, ts.URL)
	require.NoError(t, err)
	assert.Equal(t, "none", body, "an empty certificate presents none")

	set(pairFor("first"))
	body, err = get(c, ts.URL)
	require.NoError(t, err)
	assert.Equal(t, "first", body)

	set(pairFor("second"))
	body, err = get(c, ts.URL)
	require.NoError(t, err)
	assert.Equal(t, "second", body, "a new connection presents the swapped certificate")
}

func TestNewTLSClientFunc_RefusesTLS12AndACAFileItCannotUse(t *testing.T) {
	ca := newTestCA(t, "ca")
	f := writeFiles(t, ca.pem, nil, nil)
	noCert := writeFiles(t, []byte("not a certificate\n"), nil, nil)
	certPEM, keyPEM := ca.issue(t, "client")
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	present := func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil }

	c, err := api.NewTLSClientFunc(f.ca, present)
	require.NoError(t, err)
	_, err = get(c, mtlsServer(t, ca, 0).URL)
	require.NoError(t, err, "control: a current server is reached with the pair")
	_, err = get(c, mtlsServer(t, ca, tls.VersionTLS12).URL)
	assert.Error(t, err, "a TLS 1.2 server is refused although the pair is valid")

	_, err = api.NewTLSClientFunc(noCert.ca, present)
	assert.ErrorContains(t, err, noCert.ca)
}

func TestNewTLSClient_RefusesTLS12(t *testing.T) {
	ca := newTestCA(t, "ca")
	f := clientFiles(t, ca, ca)
	c, err := api.NewTLSClient(f.ca, f.cert, f.key)
	require.NoError(t, err)

	current := mtlsServer(t, ca, 0)
	control, err := get(c, current.URL)
	require.NoError(t, err)
	require.Equal(t, "ok", control)

	old := mtlsServer(t, ca, tls.VersionTLS12)
	_, err = get(c, old.URL)
	assert.Error(t, err)
}

func TestNewTLSClient_RefusesAFileItCannotUse(t *testing.T) {
	ca := newTestCA(t, "ca")
	other := newTestCA(t, "other")
	certPEM, keyPEM := ca.issue(t, "client")
	_, otherKeyPEM := other.issue(t, "client")
	good := writeFiles(t, ca.pem, certPEM, keyPEM)

	_, err := api.NewTLSClient(good.ca, good.cert, good.key)
	require.NoError(t, err, "control: the unbroken files build a client")

	missing := filepath.Join(t.TempDir(), "absent.pem")
	noCert := writeFiles(t, []byte("not a certificate\n"), certPEM, keyPEM)
	mismatch := writeFiles(t, ca.pem, certPEM, otherKeyPEM)

	tests := []struct {
		name    string
		ca      string
		cert    string
		key     string
		wantErr string
	}{
		{"missing ca", missing, good.cert, good.key, missing},
		{"missing cert", good.ca, missing, good.key, missing},
		{"missing key", good.ca, good.cert, missing, missing},
		{"ca without a certificate", noCert.ca, good.cert, good.key, noCert.ca},
		{"key of another certificate names the key", good.ca, mismatch.cert, mismatch.key, mismatch.key},
		{"key of another certificate names the cert", good.ca, mismatch.cert, mismatch.key, mismatch.cert},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := api.NewTLSClient(tt.ca, tt.cert, tt.key)
			require.Error(t, err)
			assert.Nil(t, c)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestTLSFlags_AllOrNone(t *testing.T) {
	ca := newTestCA(t, "ca")
	f := clientFiles(t, ca, ca)
	ts := mtlsServer(t, ca, 0)

	tests := []struct {
		name    string
		args    []string
		wantErr []string
	}{
		{"all three", []string{"--tls-ca", f.ca, "--tls-cert", f.cert, "--tls-key", f.key}, nil},
		{"only ca", []string{"--tls-ca", f.ca}, []string{"--tls-cert", "--tls-key"}},
		{"only cert", []string{"--tls-cert", f.cert}, []string{"--tls-ca", "--tls-key"}},
		{"only key", []string{"--tls-key", f.key}, []string{"--tls-ca", "--tls-cert"}},
		{"no ca", []string{"--tls-cert", f.cert, "--tls-key", f.key}, []string{"--tls-ca"}},
		{"no cert", []string{"--tls-ca", f.ca, "--tls-key", f.key}, []string{"--tls-cert"}},
		{"no key", []string{"--tls-ca", f.ca, "--tls-cert", f.cert}, []string{"--tls-key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			files := api.RegisterTLSFlags(fs)
			require.NoError(t, fs.Parse(tt.args))

			c, err := files.Client()
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.Nil(t, c)
				for _, w := range tt.wantErr {
					assert.Contains(t, err.Error(), w)
				}
				return
			}
			require.NoError(t, err)
			body, err := get(c, ts.URL)
			require.NoError(t, err)
			assert.Equal(t, "ok", body)
		})
	}
}

func TestTLSFlags_NoneKeepsPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(plain.Close)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	files := api.RegisterTLSFlags(fs)
	require.NoError(t, fs.Parse(nil))
	c, err := files.Client()
	require.NoError(t, err)

	body, err := get(c, plain.URL)
	require.NoError(t, err)
	assert.Equal(t, "ok", body)

	ca := newTestCA(t, "ca")
	_, err = get(c, mtlsServer(t, ca, 0).URL)
	assert.Error(t, err, "control: the plain client cannot reach a mutual TLS server")
}

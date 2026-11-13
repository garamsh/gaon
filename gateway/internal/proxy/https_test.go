package proxy_test

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// servedBy returns the certificate the proxy at addr presents for the server
// name, and the TLS connection, which the caller closes.
func servedBy(t *testing.T, addr, name string) (*tls.Conn, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	c := tls.Client(conn, &tls.Config{ServerName: name, InsecureSkipVerify: true})
	require.NoError(t, c.SetDeadline(time.Now().Add(5*time.Second)))
	if err := c.Handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// request sends a request over an open connection and returns the status.
func request(t *testing.T, c net.Conn, br *bufio.Reader, host string) int {
	t.Helper()
	_, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func httpsObjects(t *testing.T, secrets ...api.Secret) proxy.Objects {
	t.Helper()
	o := objects(t, gatewayOf(t, httpsListener("secure", 443, "app.example.com", "app-cert")),
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "echo"}))},
		map[string][]string{"echo": {echoing(t)}})
	o.Secrets = secrets
	return o
}

func TestHTTPS_ServesTheCertificateOfItsSecretAndForwardsHTTPS(t *testing.T) {
	pair := newKeyPair(t, "app.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(httpsObjects(t, pair.secret("app-cert")))
	resp, err := tlsClient(h.addr(t, 443), pair.pool()).Get("https://app.example.com/")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "proto=https ")
	assert.Contains(t, string(body), "host=app.example.com ")
}

func TestHTTPS_NewCertificateServesNewConnectionsAndOpenOnesKeepTheOld(t *testing.T) {
	first, second := newKeyPair(t, "app.example.com"), newKeyPair(t, "app.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(httpsObjects(t, first.secret("app-cert")))
	addr := h.addr(t, 443)

	open, err := servedBy(t, addr, "app.example.com")
	require.NoError(t, err)
	defer open.Close()
	require.True(t, first.cert.Equal(open.ConnectionState().PeerCertificates[0]), "setup: the connection was made with the first certificate")
	br := bufio.NewReader(open)
	require.Equal(t, http.StatusOK, request(t, open, br, "app.example.com"))

	h.Apply(httpsObjects(t, second.secret("app-cert")))

	fresh, err := servedBy(t, addr, "app.example.com")
	require.NoError(t, err)
	defer fresh.Close()
	assert.True(t, second.cert.Equal(fresh.ConnectionState().PeerCertificates[0]), "a new connection gets the new certificate")
	assert.Equal(t, http.StatusOK, request(t, open, br, "app.example.com"), "the open connection still works")
	assert.True(t, first.cert.Equal(open.ConnectionState().PeerCertificates[0]), "the open connection keeps the old certificate")
}

func TestHTTPS_InvalidPairKeepsTheLastGoodOne(t *testing.T) {
	good, other := newKeyPair(t, "app.example.com"), newKeyPair(t, "app.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(httpsObjects(t, good.secret("app-cert")))
	addr := h.addr(t, 443)

	broken := secretOf("app-cert", good.crt, other.key)
	h.Apply(httpsObjects(t, broken))
	h.Apply(httpsObjects(t, broken))
	c, err := servedBy(t, addr, "app.example.com")
	require.NoError(t, err)
	assert.True(t, good.cert.Equal(c.ConnectionState().PeerCertificates[0]), "the last good pair is still served")
	c.Close()
	assert.Equal(t, 1, strings.Count(h.logs.String(), "certificate not loaded"), "the problem is logged once")

	h.Apply(httpsObjects(t, other.secret("app-cert")))
	c, err = servedBy(t, addr, "app.example.com")
	require.NoError(t, err)
	defer c.Close()
	assert.True(t, other.cert.Equal(c.ConnectionState().PeerCertificates[0]), "control: a valid pair is loaded")
}

func TestHTTPS_NoCertificateFailsTheHandshake(t *testing.T) {
	good := newKeyPair(t, "app.example.com")
	for _, c := range []struct {
		name    string
		secrets []api.Secret
		ok      bool
	}{
		{"a valid pair is served", []api.Secret{good.secret("app-cert")}, true},
		{"an invalid pair and no earlier one fails", []api.Secret{secretOf("app-cert", []byte("junk"), []byte("junk"))}, false},
		{"a missing Secret fails", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, proxy.Config{})
			h.Apply(httpsObjects(t, c.secrets...))
			conn, err := servedBy(t, h.addr(t, 443), "app.example.com")
			if c.ok {
				require.NoError(t, err)
				conn.Close()
				return
			}
			assert.Error(t, err)
		})
	}
}

func TestHTTPS_ServerNameSelectsTheListenersCertificate(t *testing.T) {
	a, b := newKeyPair(t, "a.example.com"), newKeyPair(t, "b.example.com")
	h := newHarness(t, proxy.Config{})
	o := objects(t, gatewayOf(t,
		httpsListener("a", 443, "a.example.com", "a-cert"),
		httpsListener("b", 443, "b.example.com", "b-cert"),
	), nil, nil)
	o.Secrets = []api.Secret{a.secret("a-cert"), b.secret("b-cert")}
	h.Apply(o)
	addr := h.addr(t, 443)
	for name, want := range map[string]keyPair{"a.example.com": a, "b.example.com": b} {
		c, err := servedBy(t, addr, name)
		require.NoError(t, err, name)
		assert.True(t, want.cert.Equal(c.ConnectionState().PeerCertificates[0]), name)
		c.Close()
	}
	_, err := servedBy(t, addr, "c.example.com")
	assert.Error(t, err, "a server name no listener has gets no certificate")
}

func TestHTTPS_LogsCarryNoKeyMaterial(t *testing.T) {
	good, other := newKeyPair(t, "app.example.com"), newKeyPair(t, "app.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(httpsObjects(t, good.secret("app-cert")))
	h.Apply(httpsObjects(t, secretOf("app-cert", good.crt, other.key)))
	h.Apply(httpsObjects(t, secretOf("app-cert", []byte("junk"), good.key)))
	logs := h.logs.String()
	require.Contains(t, logs, "certificate not loaded", "setup: the problems were logged")
	for _, secret := range [][]byte{good.key, other.key, good.crt} {
		assert.NotContains(t, logs, string(secret))
	}
	assert.NotContains(t, logs, "PRIVATE KEY")
	assert.NotContains(t, logs, "junk")
}

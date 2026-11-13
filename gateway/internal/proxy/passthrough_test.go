package proxy_test

import (
	"crypto/tls"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// echoTLS starts a TLS server that echoes what it reads, with the pair, and
// returns its address.
func echoTLS(t *testing.T, pair keyPair) string {
	t.Helper()
	cert, err := tls.X509KeyPair(pair.crt, pair.key)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// countConn counts the bytes read from it.
type countConn struct {
	net.Conn
	read atomic.Int64
}

func (c *countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

// dialTLS runs a TLS handshake with addr under cfg and returns the connection,
// which counts the bytes read from the proxy, and the handshake's error.
func dialTLS(t *testing.T, addr string, cfg *tls.Config) (*tls.Conn, *countConn, error) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	cc := &countConn{Conn: raw}
	c := tls.Client(cc, cfg)
	require.NoError(t, c.SetDeadline(time.Now().Add(5*time.Second)))
	return c, cc, c.Handshake()
}

func passthroughObjects(t *testing.T, backend map[string][]string, hostnames ...string) proxy.Objects {
	t.Helper()
	return objects(t, gatewayOf(t, tlsListener("db", 8443, "")),
		[]api.CustomObject{tlsRoute(t, "db", hostnames, "db")}, backend)
}

func TestPassthrough_RoutesByServerNameWithoutTerminating(t *testing.T) {
	pair := newKeyPair(t, "db.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(passthroughObjects(t, map[string][]string{"db": {echoTLS(t, pair)}}, "db.example.com"))
	addr := h.addr(t, 8443)

	c, _, err := dialTLS(t, addr, &tls.Config{ServerName: "db.example.com", RootCAs: pair.pool()})
	require.NoError(t, err, "the handshake reaches the backend, which holds the only certificate")
	defer c.Close()
	assert.True(t, pair.cert.Equal(c.ConnectionState().PeerCertificates[0]))
	for _, msg := range []string{"hello", "again"} {
		_, err = io.WriteString(c, msg)
		require.NoError(t, err)
		got := make([]byte, len(msg))
		_, err = io.ReadFull(c, got)
		require.NoError(t, err)
		assert.Equal(t, msg, string(got))
	}
}

func TestPassthrough_ConnectionWithoutARouteIsClosedWithoutAReply(t *testing.T) {
	pair := newKeyPair(t, "db.example.com", "other.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(passthroughObjects(t, map[string][]string{"db": {echoTLS(t, pair)}}, "db.example.com"))
	addr := h.addr(t, 8443)

	t.Run("control: the routed name is served", func(t *testing.T) {
		c, _, err := dialTLS(t, addr, &tls.Config{ServerName: "db.example.com", RootCAs: pair.pool()})
		require.NoError(t, err)
		c.Close()
	})
	for _, c := range []struct {
		name string
		cfg  *tls.Config
	}{
		{"another server name", &tls.Config{ServerName: "other.example.com", RootCAs: pair.pool()}},
		{"no server name", &tls.Config{InsecureSkipVerify: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn, cc, err := dialTLS(t, addr, c.cfg)
			require.Error(t, err)
			conn.Close()
			assert.Zero(t, cc.read.Load(), "the proxy sent nothing")
		})
	}
}

func TestPassthrough_NoReadyAddressClosesTheConnection(t *testing.T) {
	pair := newKeyPair(t, "db.example.com")
	h := newHarness(t, proxy.Config{})
	h.Apply(passthroughObjects(t, map[string][]string{"db": nil}, "db.example.com"))
	conn, cc, err := dialTLS(t, h.addr(t, 8443), &tls.Config{ServerName: "db.example.com", RootCAs: pair.pool()})
	require.Error(t, err)
	conn.Close()
	assert.Zero(t, cc.read.Load())

	h.Apply(passthroughObjects(t, map[string][]string{"db": {echoTLS(t, pair)}}, "db.example.com"))
	c, _, err := dialTLS(t, h.addr(t, 8443), &tls.Config{ServerName: "db.example.com", RootCAs: pair.pool()})
	require.NoError(t, err, "control: with an address the same name is served")
	c.Close()
}

func TestPassthrough_ConnectionThatSendsNoHelloIsClosed(t *testing.T) {
	for _, c := range []struct {
		name   string
		cfg    proxy.Config
		closed bool
	}{
		{"a short hello timeout closes it", proxy.Config{HelloTimeout: 150 * time.Millisecond}, true},
		{"control: the default hello timeout leaves it open", proxy.Config{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.cfg)
			h.Apply(passthroughObjects(t, nil, "db.example.com"))
			conn, err := net.Dial("tcp", h.addr(t, 8443))
			require.NoError(t, err)
			defer conn.Close()
			assert.Equal(t, c.closed, closedWithin(t, conn, time.Second))
		})
	}
}

func TestPassthrough_CloseClosesTheOpenConnections(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	h.Apply(passthroughObjects(t, nil, "db.example.com"))
	conn, err := net.Dial("tcp", h.addr(t, 8443))
	require.NoError(t, err)
	defer conn.Close()
	require.False(t, closedWithin(t, conn, 200*time.Millisecond), "control: the connection is open before Close")
	h.Close()
	assert.True(t, closedWithin(t, conn, 2*time.Second), "Close closes the connection that is open")
}

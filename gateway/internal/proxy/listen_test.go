package proxy_test

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// accepts reports whether something listens on addr.
func accepts(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func TestListen_PortsFollowTheListenersOfTheGateway(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	backends := map[string][]string{"a": {serving(t, "a")}}
	routes := []api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"}))}
	h.Apply(objects(t, gatewayOf(t, httpListener("web", 80, "")), routes, backends))
	first := h.addr(t, 80)
	require.True(t, accepts(first), "setup: the first port is open")

	h.Apply(objects(t, gatewayOf(t, httpListener("web", 81, "")), routes, backends))
	second := h.addr(t, 81)
	assert.True(t, accepts(second), "the new port is open")
	assert.False(t, accepts(first), "the port the Gateway no longer has is closed")
}

func TestListen_ListenerThatChangesProtocolIsReopened(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	h.Apply(objects(t, gatewayOf(t, httpListener("web", 80, "")), nil, nil))
	first := h.addr(t, 80)
	h.Apply(objects(t, gatewayOf(t, tlsListener("web", 80, "")), nil, nil))
	assert.False(t, accepts(first), "the listener of the old protocol is closed")
	assert.True(t, accepts(h.addr(t, 80)), "a listener of the new protocol is open")
}

func TestListen_FailedPortIsTriedAgainByTheNextApply(t *testing.T) {
	var attempts atomic.Int32
	var lns []net.Listener
	p := proxy.New(proxy.Config{Gateway: gwName, Listen: func(int32) (net.Listener, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("address in use")
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		lns = append(lns, ln)
		return ln, err
	}}, slog.New(slog.DiscardHandler))
	t.Cleanup(p.Close)
	o := objects(t, gatewayOf(t, httpListener("web", 80, "")), nil, nil)

	p.Apply(o)
	assert.Empty(t, lns, "the first attempt failed")
	p.Apply(o)
	require.Len(t, lns, 1, "the next Apply tried again")
	assert.True(t, accepts(lns[0].Addr().String()))
	p.Apply(o)
	assert.EqualValues(t, 2, attempts.Load(), "a port that is open is not opened again")
}

func TestListen_CloseStopsListening(t *testing.T) {
	h := newHarness(t, proxy.Config{})
	addr := serveHTTP(t, h, nil, nil)
	require.True(t, accepts(addr))
	h.Close()
	assert.False(t, accepts(addr))
	_, err := http.Get("http://" + addr)
	assert.Error(t, err)
}

// temporaryError is an Accept error that net/http and the proxy retry.
type temporaryError struct{}

func (temporaryError) Error() string   { return "too many open files" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

// flakyListener returns the queued errors from Accept, one at each call, and
// then accepts as the listener it wraps does.
type flakyListener struct {
	net.Listener
	mu   sync.Mutex
	errs []error
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func TestListen_PassthroughKeepsAcceptingAfterTemporaryErrors(t *testing.T) {
	pair := newKeyPair(t, "db.example.com")
	var addr string
	p := proxy.New(proxy.Config{Gateway: gwName, Listen: func(int32) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			addr = ln.Addr().String()
		}
		return &flakyListener{Listener: ln, errs: []error{temporaryError{}, temporaryError{}, temporaryError{}}}, err
	}}, slog.New(slog.DiscardHandler))
	t.Cleanup(p.Close)
	p.Apply(passthroughObjects(t, map[string][]string{"db": {echoTLS(t, pair)}}, "db.example.com"))

	c, _, err := dialTLS(t, addr, &tls.Config{ServerName: "db.example.com", RootCAs: pair.pool()})
	require.NoError(t, err, "the port serves after the temporary errors")
	defer c.Close()
	_, err = io.WriteString(c, "x")
	require.NoError(t, err)
}

func TestListen_APortWhoseListenerFailsIsOpenedAgainByTheNextApply(t *testing.T) {
	for _, c := range []struct {
		name     string
		listener gateway.Listener
	}{
		{"an HTTP port", httpListener("web", 80, "")},
		{"a passthrough port", tlsListener("db", 8443, "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := &logBuffer{}
			var attempts atomic.Int32
			p := proxy.New(proxy.Config{Gateway: gwName, Listen: func(int32) (net.Listener, error) {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return nil, err
				}
				if attempts.Add(1) == 1 {
					return &flakyListener{Listener: ln, errs: []error{errors.New("listener broke")}}, nil
				}
				return ln, nil
			}}, slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(p.Close)
			o := objects(t, gatewayOf(t, c.listener), nil, nil)

			p.Apply(o)
			require.Eventually(t, func() bool {
				p.Apply(o)
				return attempts.Load() == 2
			}, 5*time.Second, 10*time.Millisecond, "the port is opened again")
			assert.Contains(t, logs.String(), "listener broke", "the failure is logged")
			for range 3 {
				p.Apply(o)
			}
			assert.EqualValues(t, 2, attempts.Load(), "control: a port that serves is not opened again")
		})
	}
}

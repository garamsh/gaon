package proxy_test

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

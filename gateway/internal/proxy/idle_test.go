package proxy_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// closedWithin reports whether the peer closes c within d, reading from it
// until then.
func closedWithin(t *testing.T, c net.Conn, d time.Duration) bool {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(d)))
	_, err := io.Copy(io.Discard, c)
	var ne net.Error
	return err == nil || !(errors.As(err, &ne) && ne.Timeout())
}

func TestIdle_ConnectionWithoutBytesIsClosed(t *testing.T) {
	for _, c := range []struct {
		name   string
		cfg    proxy.Config
		closed bool
	}{
		{"a short idle timeout closes it", proxy.Config{IdleTimeout: 150 * time.Millisecond}, true},
		{"control: the default idle timeout leaves it open", proxy.Config{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.cfg)
			addr := serveHTTP(t, h, nil, nil)
			conn, err := net.Dial("tcp", addr)
			require.NoError(t, err)
			defer conn.Close()
			assert.Equal(t, c.closed, closedWithin(t, conn, time.Second))
		})
	}
}

func TestIdle_StreamKeepsGoingWhileBytesMoveAndEndsWhenTheyStop(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pause := 50 * time.Millisecond
		if r.URL.Path == "/stall" {
			pause = 2 * time.Second
		}
		for range 10 {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			select {
			case <-time.After(pause):
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(backend.Close)
	h := newHarness(t, proxy.Config{IdleTimeout: 300 * time.Millisecond})
	addr := serveHTTP(t, h,
		[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "s"}))},
		map[string][]string{"s": {hostPort(t, backend)}})

	read := func(path string) (int, error) {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + addr + path)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return len(body), err
	}
	n, err := read("/steady")
	require.NoError(t, err, "control: a stream of 500ms, written to every 50ms, outlives the idle timeout of 300ms")
	assert.Equal(t, 10, n)

	n, err = read("/stall")
	assert.Error(t, err, "a stream that stops is closed")
	assert.Less(t, n, 10)
}

func TestIdle_KeepAliveConnectionIsClosedAfterTheKeepAlive(t *testing.T) {
	for _, c := range []struct {
		name   string
		cfg    proxy.Config
		closed bool
	}{
		{"a short keep-alive closes it", proxy.Config{KeepAlive: 150 * time.Millisecond}, true},
		{"control: the default keep-alive leaves it open", proxy.Config{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.cfg)
			addr := serveHTTP(t, h,
				[]api.CustomObject{httpRoute(t, "app", nil, true, rule(t, ruleTo{prefixes: []string{"/"}, backend: "a"}))},
				map[string][]string{"a": {serving(t, "a")}})
			conn, err := net.Dial("tcp", addr)
			require.NoError(t, err)
			defer conn.Close()
			require.Equal(t, http.StatusOK, request(t, conn, bufio.NewReader(conn), "app.example.com"), "setup: one request is served on the connection")
			assert.Equal(t, c.closed, closedWithin(t, conn, time.Second))
		})
	}
}

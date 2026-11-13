package apiserver_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
)

// silentMember forwards TCP to one etcd member until swallow, then reads and
// discards what both sides send without closing a connection, as a host that
// stopped without a reset does. Before swallow it closes a connection whose
// other side closed.
type silentMember struct {
	addr string
	mu   sync.Mutex
	dead bool
	open int
}

func startSilentMember(t *testing.T, target string) *silentMember {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	m := &silentMember{addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			t.Cleanup(func() { _ = c.Close(); _ = up.Close() })
			m.mu.Lock()
			m.open++
			m.mu.Unlock()
			go func() {
				m.pipe(up, c)
				m.mu.Lock()
				m.open--
				m.mu.Unlock()
			}()
			go m.pipe(c, up)
		}
	}()
	return m
}

func (m *silentMember) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		m.mu.Lock()
		dead := m.dead
		m.mu.Unlock()
		if n > 0 && !dead {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				err = werr
			}
		}
		if err != nil {
			if !dead {
				_ = src.Close()
				_ = dst.Close()
			}
			return
		}
	}
}

func (m *silentMember) swallow() {
	m.mu.Lock()
	m.dead = true
	m.mu.Unlock()
}

func (m *silentMember) connections() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open
}

// writeBound is the time the writes after a member stops are given to be
// stored, all together.
const writeBound = 10 * time.Second

// writesAfterAMemberStops runs an API server over a three-member cluster whose
// first member is behind a forwarder, stops that member with its connections
// left open, and reports whether every one of several writes is stored within
// writeBound, retrying a write that fails. The etcd client holds a connection
// to each endpoint and spreads its requests over them, so the writes put
// requests in flight on the silent connection.
func writesAfterAMemberStops(t *testing.T, keepAliveTime time.Duration) bool {
	t.Helper()
	cluster := startEtcdCluster(t, 3)
	silent := startSilentMember(t, strings.TrimPrefix(cluster.endpoints[0], "http://"))
	endpoints := append([]string{"http://" + silent.addr}, cluster.endpoints[1:]...)
	s, err := startServerWith(t, apiserver.Config{
		Endpoints:            endpoints,
		DialTimeout:          2 * time.Second,
		EtcdKeepAliveTime:    keepAliveTime,
		EtcdKeepAliveTimeout: time.Second,
	})
	require.NoError(t, err)
	s.createMicroVM(t, microvm("before"))
	require.Eventually(t, func() bool { return silent.connections() > 0 }, 5*time.Second, 20*time.Millisecond,
		"the client holds a connection to the member that is to stop")

	silent.swallow()
	cluster.stop(0)

	ctx, cancel := context.WithTimeout(context.Background(), writeBound)
	defer cancel()
	for i := range 12 {
		stored := false
		for ctx.Err() == nil && !stored {
			_, err := s.api.PutMicroVM(ctx, microvm(fmt.Sprintf("after-%d", i)))
			stored = err == nil
		}
		if !stored {
			return false
		}
	}
	return true
}

func TestEtcdKeepAlive_EveryWriteIsStoredWithinTheBoundAfterAMemberStops(t *testing.T) {
	require.True(t, writesAfterAMemberStops(t, time.Second))
}

func TestEtcdKeepAlive_WithoutThePingAWriteIsNotStoredWithinTheBound(t *testing.T) {
	require.False(t, writesAfterAMemberStops(t, 0), "control: the same stop without the ping leaves a write hanging")
}

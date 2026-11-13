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
// stopped without a reset does.
type silentMember struct {
	addr string
	mu   sync.Mutex
	dead bool
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
			go m.pipe(c, up)
			go m.pipe(up, c)
		}
	}()
	return m
}

func (m *silentMember) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			m.mu.Lock()
			dead := m.dead
			m.mu.Unlock()
			if !dead {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (m *silentMember) swallow() {
	m.mu.Lock()
	m.dead = true
	m.mu.Unlock()
}

// writeBound is the time a write is given to be stored after a member stops.
const writeBound = 10 * time.Second

// writeAfterTheConnectedMemberStops runs an API server over a three-member
// cluster whose first endpoint is a forwarder to member 0, stops that member
// with its connections left open, and reports whether a write is stored within
// writeBound, retrying a write that fails.
func writeAfterTheConnectedMemberStops(t *testing.T, keepAliveTime time.Duration) bool {
	t.Helper()
	cluster := startEtcdCluster(t, 3)
	member := startSilentMember(t, strings.TrimPrefix(cluster.endpoints[0], "http://"))
	endpoints := append([]string{"http://" + member.addr}, cluster.endpoints[1:]...)
	s, err := startServerWith(t, apiserver.Config{
		Endpoints:            endpoints,
		DialTimeout:          2 * time.Second,
		EtcdKeepAliveTime:    keepAliveTime,
		EtcdKeepAliveTimeout: time.Second,
	})
	require.NoError(t, err)
	s.createMicroVM(t, microvm("before"))

	member.swallow()
	cluster.stop(0)

	ctx, cancel := context.WithTimeout(context.Background(), writeBound)
	defer cancel()
	for i := 0; ctx.Err() == nil; i++ {
		if _, err := s.api.PutMicroVM(ctx, microvm(fmt.Sprintf("after-%d", i))); err == nil {
			return true
		}
	}
	return false
}

func TestEtcdKeepAlive_AWriteIsStoredWithinTheBoundAfterTheConnectedMemberStops(t *testing.T) {
	require.True(t, writeAfterTheConnectedMemberStops(t, time.Second))
}

func TestEtcdKeepAlive_WithoutThePingNoWriteIsStoredWithinTheBound(t *testing.T) {
	require.False(t, writeAfterTheConnectedMemberStops(t, 0), "control: the same stop without the ping leaves the write hanging")
}

package nodeagent

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/lib/plugin/plugintest"
)

func TestConfig_PluginTimeoutIsTenSecondsUnlessSet(t *testing.T) {
	assert.Equal(t, 10*time.Second, Config{}.pluginTimeout())
	assert.Equal(t, 150*time.Millisecond, Config{PluginTimeout: 150 * time.Millisecond}.pluginTimeout())
}

// hang accepts connections on the socket of the plugin kind-name in dir and
// never answers on them.
func hang(t *testing.T, dir, kind, name string) {
	t.Helper()
	lis, err := net.Listen("unix", filepath.Join(dir, kind+"-"+name+".sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
}

func TestDialPlugins_ACallToAPluginThatNeverAnswersReturnsWithinTheTimeout(t *testing.T) {
	dir := plugintest.SocketDir(t)
	hang(t, dir, "network", "hung")
	hang(t, dir, "storage", "hung")
	p, err := dialPlugins(Config{PluginDir: dir, NetworkPlugin: "hung", StoragePlugin: "hung", PluginTimeout: 200 * time.Millisecond})
	require.NoError(t, err)
	defer p.close()

	// The caller's own deadline is far later, so only the plugin timeout ends a call early.
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	m := &api.MicroVM{}
	for name, call := range map[string]func() error{
		"Policy":  func() error { _, err := p.network.Policy(ctx, m); return err },
		"Enforce": func() error { return p.network.(plugin.Enforcer).Enforce(ctx, nil) },
		"Attach":  func() error { _, err := p.storage.Attach(ctx, m); return err },
		"Release": func() error { return p.storage.Release(ctx, "id") },
	} {
		start := time.Now()
		err := call()
		assert.Error(t, err, name)
		assert.Less(t, time.Since(start), 4*time.Second, "%s returns near the injected timeout, not the caller's", name)
	}
}

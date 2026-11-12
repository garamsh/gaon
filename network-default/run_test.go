package networkdefault_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/plugin"
	networkdefault "github.com/garamsh/gaon/network-default"
)

func TestRun_ServesThePolicyOfItsPrefixesOnTheSocketOfTheNamedPlugin(t *testing.T) {
	dir, err := os.MkdirTemp("", "nd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- networkdefault.Run(ctx, networkdefault.Config{PluginDir: dir, Egress: []string{"10.0.0.0/8"}, Ingress: []string{"192.168.0.0/16"}})
	}()
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-done)
	})
	c, err := plugin.DialNetwork(dir, "default")
	require.NoError(t, err)
	defer c.Close()

	require.Eventually(t, func() bool {
		_, err := c.Policy(t.Context(), microvm([]int32{80}))
		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	got, err := c.Policy(t.Context(), microvm([]int32{80}))

	require.NoError(t, err)
	assert.Equal(t, []plugin.NetworkRule{{Prefix: "10.0.0.0/8", Protocol: plugin.Any}}, got.Egress)
	require.Len(t, got.Ingress, 1)
	assert.Equal(t, "192.168.0.0/16", got.Ingress[0].Prefix)
}

func TestRun_StopsOnAPrefixThatIsNotACIDR(t *testing.T) {
	err := networkdefault.Run(t.Context(), networkdefault.Config{PluginDir: t.TempDir(), Egress: []string{"nonsense"}})

	assert.Error(t, err)
}

package plugintest_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/lib/plugin/plugintest"
)

func TestSocketDir_LeavesRoomForASocketPathWhateverTMPDIRIs(t *testing.T) {
	long := t.TempDir() + strings.Repeat("/long", 30)
	require.NoError(t, os.MkdirAll(long, 0o700))
	require.Greater(t, len(long), plugin.MaxSocketPathLen, "the long TMPDIR must be over the limit")

	for name, tmp := range map[string]string{"a short TMPDIR": "/tmp", "a long TMPDIR": long} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", tmp)
			dir := plugintest.SocketDir(t)

			info, err := os.Stat(dir)
			require.NoError(t, err)
			assert.True(t, info.IsDir())
			_, err = plugin.SocketPath(dir, plugin.KindStorage, strings.Repeat("a", 40))
			assert.NoError(t, err)
		})
	}
}

func TestSocketDir_IsRemovedWhenTheTestEnds(t *testing.T) {
	var dir string
	t.Run("owner", func(t *testing.T) { dir = plugintest.SocketDir(t) })

	_, err := os.Stat(dir)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// Package plugintest gives tests a directory a Unix socket can be bound in.
package plugintest

import (
	"os"
	"testing"

	"github.com/garamsh/gaon/lib/plugin"
)

// room is what a socket path adds to its directory: a subdirectory, a kind,
// a name and the suffix.
const room = 64

// SocketDir returns a new directory, removed when the test ends, with room
// for a socket path under it. It is made under TMPDIR, or under /tmp when
// TMPDIR is too long for that.
func SocketDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir)+room > plugin.MaxSocketPathLen {
		_ = os.RemoveAll(dir)
		if dir, err = os.MkdirTemp("/tmp", "gs"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

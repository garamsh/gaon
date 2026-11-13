package certificate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// observeSyncDir replaces syncDir with one that records, at each call, the
// directory it is given and what that directory holds, and returns result.
func observeSyncDir(t *testing.T, result error) (dirs *[]string, held *[][]string) {
	t.Helper()
	dirs, held = &[]string{}, &[][]string{}
	orig := syncDir
	t.Cleanup(func() { syncDir = orig })
	syncDir = func(dir string) error {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		*dirs = append(*dirs, dir)
		*held = append(*held, names)
		return result
	}
	return dirs, held
}

func TestWritePair_SyncsTheDirectoryAfterTheRename(t *testing.T) {
	dir := t.TempDir()
	dirs, held := observeSyncDir(t, nil)

	require.NoError(t, writePair(dir, []byte("key\n"), []byte("cert\n")))

	assert.Equal(t, []string{dir}, *dirs, "the directory that holds the pair is synced once")
	assert.Equal(t, [][]string{{PairFile}}, *held, "at the sync the pair is at its name and no temporary file is left")
}

func TestWritePair_ReturnsTheDirectorySyncFailure(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("disk gone")
	_, _ = observeSyncDir(t, boom)

	err := writePair(dir, []byte("key\n"), []byte("cert\n"))

	require.ErrorIs(t, err, boom)
	assert.FileExists(t, filepath.Join(dir, PairFile), "control: the rename ran, so the failure is the sync's")
}

func TestSyncDir_FailsOnADirectoryThatIsMissing(t *testing.T) {
	require.NoError(t, syncDir(t.TempDir()), "control: an existing directory syncs")
	assert.Error(t, syncDir(filepath.Join(t.TempDir(), "missing")))
}

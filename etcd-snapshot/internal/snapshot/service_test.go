package snapshot_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot"
)

// withHash is a database of one 512 byte page followed by the SHA-256 of the
// page, the shape etcd streams.
func withHash(db []byte) []byte {
	sum := sha256.Sum256(db)
	return append(append([]byte{}, db...), sum[:]...)
}

func page(fill byte) []byte {
	b := make([]byte, 512)
	for i := range b {
		b[i] = fill
	}
	return b
}

type fakeSource struct {
	content  []byte
	takeErr  error
	revision snapshot.Revision
	revCalls int
}

func (s *fakeSource) Take(_ context.Context, path string) error {
	if s.takeErr != nil {
		return s.takeErr
	}
	return os.WriteFile(path, s.content, 0o600)
}

func (s *fakeSource) Revision(string) (snapshot.Revision, error) {
	s.revCalls++
	return s.revision, nil
}

type fakeStore struct {
	err  error
	puts []snapshot.Object
	body [][]byte
}

func (s *fakeStore) Put(_ context.Context, o snapshot.Object) error {
	if s.err != nil {
		return s.err
	}
	b, err := io.ReadAll(o.Body)
	if err != nil {
		return err
	}
	s.puts, s.body = append(s.puts, o), append(s.body, b)
	return nil
}

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

func newService(t *testing.T, src *fakeSource, store *fakeStore) (snapshot.Service, string) {
	t.Helper()
	dir := t.TempDir()
	clock := fakeClock{time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("KST", 9*3600))}
	return snapshot.NewService(src, store, clock, snapshot.Config{Dir: dir, Prefix: "etcd/"}, slog.New(slog.NewTextHandler(io.Discard, nil))), dir
}

func TestKey(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("KST", 9*3600))

	assert.Equal(t, "etcd/20260303T200607Z-rev42.db", snapshot.Key("etcd/", at, 42))
	assert.Equal(t, "20260303T200607Z-rev7.db", snapshot.Key("", at, 7))
}

func TestRun_PutsTheCheckedSnapshotWithItsDigest(t *testing.T) {
	content := withHash(page('a'))
	src := &fakeSource{content: content, revision: 42}
	store := &fakeStore{}
	svc, dir := newService(t, src, store)

	require.NoError(t, svc.Run(context.Background()))

	require.Len(t, store.puts, 1)
	o := store.puts[0]
	assert.Equal(t, "etcd/20260303T200607Z-rev42.db", o.Key)
	assert.Equal(t, int64(len(content)), o.Size)
	assert.Equal(t, sha256.Sum256(content), o.SHA256)
	assert.Equal(t, content, store.body[0], "the whole file, from its start")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the snapshot file is removed")
}

func TestRun_PutsNothingForASnapshotThatFailsItsCheck(t *testing.T) {
	flipped := withHash(page('a'))
	flipped[10] ^= 0xff
	noHash := page('a')

	for name, content := range map[string][]byte{
		"a database that differs from its hash":  flipped,
		"a file with no hash after the database": noHash,
		"an empty file":                          {},
	} {
		t.Run(name, func(t *testing.T) {
			src := &fakeSource{content: content, revision: 1}
			store := &fakeStore{}
			svc, dir := newService(t, src, store)

			err := svc.Run(context.Background())

			require.ErrorContains(t, err, "check snapshot")
			assert.Empty(t, store.puts)
			assert.Zero(t, src.revCalls)
			entries, rerr := os.ReadDir(dir)
			require.NoError(t, rerr)
			assert.Empty(t, entries)
		})
	}
}

func TestRun_ReturnsTheFailureOfTheSourceAndOfTheStore(t *testing.T) {
	boom := errors.New("boom")

	src := &fakeSource{takeErr: boom}
	store := &fakeStore{}
	svc, _ := newService(t, src, store)
	require.ErrorIs(t, svc.Run(context.Background()), boom)
	assert.Empty(t, store.puts)

	src = &fakeSource{content: withHash(page('b')), revision: 3}
	store = &fakeStore{err: boom}
	svc, dir := newService(t, src, store)
	require.ErrorIs(t, svc.Run(context.Background()), boom)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

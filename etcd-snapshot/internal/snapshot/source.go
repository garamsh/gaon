package snapshot

import "context"

// Source takes a snapshot of the store and reads what it holds.
type Source interface {
	// Take writes one consistent snapshot to the file at path, including the
	// integrity hash etcd appends.
	Take(ctx context.Context, path string) error
	// Revision returns the revision of the store the snapshot at path holds.
	Revision(path string) (Revision, error)
}

package resourceapi

import (
	"context"
	"errors"
)

// errFenceMoved reports that a key a create was fenced on changed between the
// read that decided the create and the create. The caller reads again.
var errFenceMoved = errors.New("a key the write is fenced on was changed")

// fenceAttempts is how many times a write fenced on a key reads again after the
// key moved before it reports a conflict.
const fenceAttempts = 5

// fencedStore is a Store whose Create also holds the conditions of fence, in the
// same step. It orders a create against writes made through other API server
// processes, which a lock in this process cannot.
type fencedStore struct {
	Store
	fence []Write
}

// Create stores value under key if the key is absent and every fence key is at
// the revision it names. It returns ErrExists where the key is present, and
// errFenceMoved where a fence key is at another revision.
func (f fencedStore) Create(ctx context.Context, key string, value []byte) (int64, error) {
	writes := append([]Write{{Key: key, Value: value, Revision: 0}}, f.fence...)
	rev, err := f.Store.Transact(ctx, writes)
	if !errors.Is(err, ErrStale) {
		return rev, err
	}
	if _, err := f.Store.Get(ctx, key); err == nil {
		return 0, ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	return 0, errFenceMoved
}

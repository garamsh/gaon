package resourceapi

import (
	"context"
	"errors"

	"github.com/garamsh/gaon/lib/api"
)

// The failures a Store reports, matched with errors.Is.
var (
	ErrNotFound  = errors.New("key not found")
	ErrExists    = errors.New("key exists")
	ErrStale     = errors.New("key is at another revision")
	ErrCompacted = errors.New("revision is compacted")
)

// Entry is a value held under a key and the revision it was last modified at.
type Entry struct {
	Key      string
	Value    []byte
	Revision int64
}

// Event is one change under a prefix, or the end of a watch. Err is set on
// the last event of a stream, and the other fields are then unset. For a
// Deleted event, Entry holds the last value and the revision of the deletion.
type Event struct {
	Type  api.EventType
	Entry Entry
	Err   error
}

// Write is one write of a Transact: Value replaces what is under Key if it is
// at Revision. A nil Value writes nothing and only holds the condition.
type Write struct {
	Key      string
	Value    []byte
	Revision int64
}

// Store holds the serialised objects, each at the revision it was last
// modified at. Every write is conditioned on a revision.
type Store interface {
	// Get returns the entry under key, or ErrNotFound.
	Get(ctx context.Context, key string) (Entry, error)
	// List returns the entries under prefix and the revision they were read at.
	List(ctx context.Context, prefix string) ([]Entry, int64, error)
	// Create stores value under key and returns its revision, or ErrExists.
	Create(ctx context.Context, key string, value []byte) (int64, error)
	// Update replaces the value under key if it is at revision, and returns
	// the new revision. It returns ErrNotFound or ErrStale otherwise.
	Update(ctx context.Context, key string, value []byte, revision int64) (int64, error)
	// Delete removes key if it is at revision, and returns the removed entry
	// with the revision of the deletion. It returns ErrNotFound or ErrStale
	// otherwise.
	Delete(ctx context.Context, key string, revision int64) (Entry, error)
	// Transact makes every write of writes in one step, if each key is at the
	// revision its write names, and returns the revision they all got. It
	// writes nothing and returns ErrStale if any key is at another revision,
	// or is absent.
	Transact(ctx context.Context, writes []Write) (int64, error)
	// Watch sends the changes under prefix made after revision, until ctx ends
	// or an event with Err set closes the channel. ErrCompacted means revision
	// is older than the Store keeps.
	Watch(ctx context.Context, prefix string, revision int64) (<-chan Event, error)
}

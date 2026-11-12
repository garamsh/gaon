package snapshot

import "time"

// Clock tells the time a snapshot is named for.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

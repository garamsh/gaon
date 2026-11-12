package garbagecollector

import "time"

// Clock paces the sweeps.
type Clock interface {
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

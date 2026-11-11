package nodelifecycle

import "time"

// Clock is the observer's monotonic clock.
type Clock interface {
	// Now returns the current time, carrying a monotonic reading.
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

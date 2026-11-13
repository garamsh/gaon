package endpoints

import "time"

// Clock paces the retries.
type Clock interface {
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

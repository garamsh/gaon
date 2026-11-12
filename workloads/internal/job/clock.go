package job

import "time"

// Clock paces the sweeps and stamps the conditions.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

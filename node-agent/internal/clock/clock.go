// Package clock is the node agent's source of time and delay.
package clock

import (
	"context"
	"time"
)

// Clock reads the monotonic clock and waits on it.
type Clock interface {
	// Now returns the current time, carrying a monotonic reading.
	Now() time.Time
	// Sleep waits for d or until ctx ends, and returns the context's error
	// in that case.
	Sleep(ctx context.Context, d time.Duration) error
}

// System is the Clock of the running process.
type System struct{}

// Now returns time.Now.
func (System) Now() time.Time { return time.Now() }

// Sleep waits on a timer.
func (System) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Package systemclock is the Clock backed by the operating system.
package systemclock

import "time"

// Clock reads the system clock.
type Clock struct{}

// Now returns the current time.
func (Clock) Now() time.Time { return time.Now() }

// After waits on a timer of the system clock.
func (Clock) After(d time.Duration) <-chan time.Time { return time.After(d) }

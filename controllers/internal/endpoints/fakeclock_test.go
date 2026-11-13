package endpoints_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is a Clock whose timers fire only when a test advances it.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	calls   int
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward by d and fires every timer now due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			kept = append(kept, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = kept
}

func (c *fakeClock) timers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// waitTimersAbove waits until the controller has asked for more than n
// timers.
func (c *fakeClock) waitTimersAbove(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return c.timers() > n }, 5*time.Second, time.Millisecond, "the controller did not wait on a timer")
}

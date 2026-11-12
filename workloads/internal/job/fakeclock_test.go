package job_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is a Clock whose timers fire when a test says so.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	calls int
	timer chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After counts the call and returns a channel that fires on the next tick.
func (c *fakeClock) After(time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.timer = make(chan time.Time, 1)
	return c.timer
}

// sweepsAbove waits until the service has finished more than n sweeps.
func (c *fakeClock) sweepsAbove(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.calls > n
	}, 5*time.Second, time.Millisecond, "the service did not finish a sweep")
}

// tick starts the next sweep and returns once it has finished.
func (c *fakeClock) tick(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	n, timer := c.calls, c.timer
	c.mu.Unlock()
	timer <- c.Now()
	c.sweepsAbove(t, n)
}

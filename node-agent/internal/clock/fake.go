package clock

import (
	"context"
	"sync"
	"time"
)

// Fake is a Clock that moves only when told to: Advance moves it, and Sleep
// moves it by the time slept and returns at once.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake reading start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake time forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Sleep advances the fake time by d, or returns ctx's error when it ended.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.Advance(d)
	return nil
}

package api

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Clock is the time LeaderElector reads and waits on. A test injects its own.
type Clock interface {
	// Now returns the current time. An elapsed time is the difference of two
	// readings, so the reading must carry a monotonic clock, as time.Now does.
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the Clock of the machine.
type SystemClock struct{}

// Now returns time.Now.
func (SystemClock) Now() time.Time { return time.Now() }

// After returns time.After(d).
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// LeaseClient is the part of Client a LeaderElector calls. Call it on a client
// whose root is the coordination group's.
type LeaseClient interface {
	GetCoordinationLease(ctx context.Context, name string) (*CoordinationLease, error)
	PutCoordinationLease(ctx context.Context, l *CoordinationLease) (*CoordinationLease, error)
}

// LeaderElector elects one holder of the CoordinationLease named Name among the
// processes that run it with different Identities.
//
// A holder renews the Lease every RetryPeriod and may act only until its last
// acknowledged renewal started RenewDeadline ago. A candidate takes the Lease
// when it has watched the same ResourceVersion for the Lease's duration on its
// own Clock, so the old holder has stopped acting, RenewDeadline being shorter
// than Duration, before another starts. A candidate sees a holder's last renewal
// at the first poll after it, and acts at the first poll at or after Duration
// from then, so a takeover comes at least Duration and at most Duration plus two
// RetryPeriods after the last renewal.
type LeaderElector struct {
	Leases LeaseClient
	// Name is the name of the Lease.
	Name string
	// Identity names this process and differs between the processes that
	// contend. A holder with the same Identity is taken to be this process.
	Identity string
	// Duration is how long a Lease no one renews stays held, in whole seconds.
	Duration time.Duration
	// RenewDeadline is how long a holder keeps acting without a renewal the
	// server acknowledged. It is less than Duration.
	RenewDeadline time.Duration
	// RetryPeriod is the time between attempts to take or to renew the Lease.
	// It is less than RenewDeadline.
	RetryPeriod time.Duration
	Clock       Clock
}

// Validate reports why e cannot run.
func (e *LeaderElector) Validate() error {
	switch {
	case e.Leases == nil || e.Clock == nil:
		return errors.New("leader election: Leases and Clock are required")
	case e.Name == "" || e.Identity == "":
		return errors.New("leader election: Name and Identity are required")
	case e.Duration < time.Second || e.Duration%time.Second != 0:
		return fmt.Errorf("leader election: duration %s is not a whole number of seconds, at least one", e.Duration)
	case e.RetryPeriod <= 0 || e.RenewDeadline <= e.RetryPeriod || e.Duration <= e.RenewDeadline:
		return fmt.Errorf("leader election: want 0 < retry period < renew deadline < duration, have %s, %s, %s",
			e.RetryPeriod, e.RenewDeadline, e.Duration)
	}
	return nil
}

// Run contends for the Lease until ctx ends. Each time this process takes it,
// Run calls lead with a context that ends when the process may no longer act:
// the Lease was taken by another, a renewal was not acknowledged within
// RenewDeadline, or ctx ended. Run waits for lead to return, then contends
// again, so lead is called once per term and never twice at once. When ctx ends
// Run releases the Lease if it still holds it, so another takes it at once.
func (e *LeaderElector) Run(ctx context.Context, lead func(ctx context.Context)) error {
	if err := e.Validate(); err != nil {
		return err
	}
	var seen observation
	for ctx.Err() == nil {
		held, took, ok := e.acquire(ctx, &seen)
		if !ok {
			return ctx.Err()
		}
		e.lead(ctx, held, took, lead)
		seen = observation{}
	}
	return ctx.Err()
}

// observation is the version of the Lease a candidate saw and when it first saw it.
type observation struct {
	version ResourceVersion
	since   time.Time
}

// acquire tries every RetryPeriod until this process holds the Lease or ctx ends.
func (e *LeaderElector) acquire(ctx context.Context, seen *observation) (*CoordinationLease, time.Time, bool) {
	for {
		start := e.Clock.Now()
		if held, ok := e.tryAcquire(ctx, seen, start); ok {
			return held, start, true
		}
		select {
		case <-ctx.Done():
			return nil, time.Time{}, false
		case <-e.Clock.After(e.RetryPeriod):
		}
	}
}

func (e *LeaderElector) tryAcquire(ctx context.Context, seen *observation, now time.Time) (*CoordinationLease, bool) {
	cur, err := e.Leases.GetCoordinationLease(ctx, e.Name)
	if errors.Is(err, ErrNotFound) {
		return e.write(ctx, &CoordinationLease{
			TypeMeta: TypeMeta{APIVersion: CoordinationGroupVersion, Kind: KindCoordinationLease},
			Metadata: ObjectMeta{Name: e.Name},
		}, now)
	}
	if err != nil {
		return nil, false
	}
	if cur.Metadata.ResourceVersion != seen.version {
		*seen = observation{version: cur.Metadata.ResourceVersion, since: now}
	}
	free := cur.Spec.Holder == "" || cur.Spec.Holder == e.Identity
	lapsed := now.Sub(seen.since) >= time.Duration(cur.Spec.DurationSeconds)*time.Second
	if !free && !lapsed {
		return nil, false
	}
	return e.write(ctx, cur, now)
}

// write puts l with this process as holder, renewed at now. It reports the
// Lease the server stored.
func (e *LeaderElector) write(ctx context.Context, l *CoordinationLease, now time.Time) (*CoordinationLease, bool) {
	next := *l
	next.Spec = CoordinationLeaseSpec{Holder: e.Identity, RenewTime: now.UTC(), DurationSeconds: int32(e.Duration / time.Second)}
	stored, err := e.Leases.PutCoordinationLease(ctx, &next)
	if err != nil {
		return nil, false
	}
	return stored, true
}

// lead runs the term of a holder that holds the Lease as held, which it took
// by a write begun at took on the elector's clock. The Lease's own renew time is
// the server's echo of a wall-clock value and carries no reading of that clock.
func (e *LeaderElector) lead(ctx context.Context, held *CoordinationLease, took time.Time, lead func(context.Context)) {
	termCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		lead(termCtx)
	}()

	renewed := took
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-done
			e.release(held)
			return
		case <-done:
			// lead returned on its own: give the Lease back rather than hold it idle.
			e.release(held)
			return
		case <-e.Clock.After(e.RetryPeriod):
		}
		attempt := e.Clock.Now()
		next, ok := e.renew(ctx, held, attempt, renewed.Add(e.RenewDeadline))
		if !ok {
			cancel()
			<-done
			return
		}
		held, renewed = next, attempt
	}
}

// renew writes held again with the renew time at, and reports whether the
// holder may keep acting. It fails when another holder wrote the Lease first,
// or when no write was acknowledged before deadline.
func (e *LeaderElector) renew(ctx context.Context, held *CoordinationLease, at, deadline time.Time) (*CoordinationLease, bool) {
	expired := e.Clock.After(deadline.Sub(e.Clock.Now()))
	for {
		callCtx, cancel := context.WithCancel(ctx)
		type result struct {
			l   *CoordinationLease
			err error
		}
		out := make(chan result, 1)
		go func() {
			next := *held
			next.Spec.RenewTime = at.UTC()
			l, err := e.Leases.PutCoordinationLease(callCtx, &next)
			out <- result{l, err}
		}()
		select {
		case r := <-out:
			cancel()
			if r.err == nil {
				return r.l, true
			}
			if errors.Is(r.err, ErrConflict) || errors.Is(r.err, ErrAlreadyExists) || errors.Is(r.err, ErrNotFound) {
				return nil, false
			}
		case <-expired:
			cancel()
			return nil, false
		case <-ctx.Done():
			cancel()
			return nil, false
		}
		select {
		case <-e.Clock.After(e.RetryPeriod):
		case <-ctx.Done():
			return nil, false
		}
		if !e.Clock.Now().Before(deadline) {
			return nil, false
		}
	}
}

// release writes the Lease with no holder, if it is still at the version this
// process wrote. A failure is not reported: the Lease then lapses by its duration.
func (e *LeaderElector) release(held *CoordinationLease) {
	ctx, cancel := context.WithTimeout(context.Background(), e.RenewDeadline)
	defer cancel()
	next := *held
	next.Spec.Holder = ""
	next.Spec.RenewTime = time.Time{}
	_, _ = e.Leases.PutCoordinationLease(ctx, &next)
}

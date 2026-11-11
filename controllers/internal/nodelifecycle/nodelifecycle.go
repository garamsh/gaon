// Package nodelifecycle confirms when a Node's Lease has expired and records
// it in the Node's LeaseExpired condition.
package nodelifecycle

import (
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// RetryDelay is how long the controller waits before it retries a failed
// write or a failed list or watch.
const RetryDelay = 5 * time.Second

// The reasons the controller gives the LeaseExpired condition.
const (
	reasonLeaseExpired = "LeaseExpired"
	reasonLeaseRenewed = "LeaseRenewed"
)

// Config holds the numbers the controller does not decide.
type Config struct {
	// Margin is added to a Lease's duration before the Lease counts as expired.
	Margin time.Duration
}

// observation is what the controller knows of one Lease: the version it first
// saw and when, on its own monotonic clock, and what it knows of the Node's
// LeaseExpired condition.
type observation struct {
	version  api.ResourceVersion
	seen     time.Time
	validity time.Duration

	// known is false until the controller has read the Node's condition.
	known bool
	// marked is whether LeaseExpired is true, once known.
	marked bool
	// retryAt is the earliest time a failed write is tried again.
	retryAt time.Time
}

// expiresAt is the first instant the Lease counts as expired.
func (o *observation) expiresAt() time.Time {
	return o.seen.Add(o.validity)
}

// want is the status LeaseExpired should have at now.
func (o *observation) want(now time.Time) api.ConditionStatus {
	if now.Before(o.expiresAt()) {
		return api.ConditionFalse
	}
	return api.ConditionTrue
}

// settled reports whether the Node already holds the status wanted at now.
func (o *observation) settled(now time.Time) bool {
	return o.known && o.marked == (o.want(now) == api.ConditionTrue)
}

// Package liveness renews the node's Lease and mvmd's fencing deadline so
// that mvmd's recorded expiry plus δ never passes the Lease's validity.
package liveness

import (
	"errors"
	"fmt"
	"time"
)

// Params are the coupled durations of the Lease and the fencing deadline.
type Params struct {
	// LeaseDuration is how long the Lease is valid after a renewal. A whole
	// number of seconds.
	LeaseDuration time.Duration
	// RenewPeriod is the time between two renewal rounds.
	RenewPeriod time.Duration
	// DeadlineLength is the length each deadline renewal asks mvmd for.
	DeadlineLength time.Duration
	// Delta is δ, how long a MicroVM can run past mvmd's recorded expiry.
	Delta time.Duration
	// RPCTimeout bounds each call of a renewal round.
	RPCTimeout time.Duration
}

// Validate checks that every duration is positive and that they fit: a
// deadline renewal sent in the round of a Lease renewal must end, with δ,
// inside the Lease, and the deadline must outlast the gap between rounds.
func (p Params) Validate() error {
	for name, d := range map[string]time.Duration{
		"lease duration": p.LeaseDuration, "renew period": p.RenewPeriod,
		"deadline length": p.DeadlineLength, "delta": p.Delta, "rpc timeout": p.RPCTimeout,
	} {
		if d <= 0 {
			return fmt.Errorf("%s is %s, want a positive duration", name, d)
		}
	}
	if p.LeaseDuration%time.Second != 0 {
		return fmt.Errorf("lease duration %s is not a whole number of seconds", p.LeaseDuration)
	}
	if need := p.RenewPeriod + 2*p.RPCTimeout; p.DeadlineLength <= need {
		return fmt.Errorf("deadline length %s must exceed renew period plus two rpc timeouts, %s", p.DeadlineLength, need)
	}
	if need := p.DeadlineLength + p.Delta + 2*p.RPCTimeout; p.LeaseDuration < need {
		return fmt.Errorf("lease duration %s must be at least deadline length plus delta plus two rpc timeouts, %s", p.LeaseDuration, need)
	}
	return nil
}

var errNoValidity = errors.New("the Lease has no validity left for a deadline renewal")

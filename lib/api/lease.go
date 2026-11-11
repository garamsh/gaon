package api

import "time"

// Lease is a node's liveness: its holder, the time it last renewed and for
// how long. The Node Agent renews it by compare-and-swap.
type Lease struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Spec     LeaseSpec  `json:"spec"`
}

// LeaseSpec is what the holder of a Lease writes. RenewTime is the holder's
// own clock reading and is not compared with another machine's clock.
type LeaseSpec struct {
	Holder          string    `json:"holder"`
	RenewTime       time.Time `json:"renewTime"`
	DurationSeconds int32     `json:"durationSeconds"`
}

// Validate checks the identity of l.
func (l *Lease) Validate() error {
	return validateIdentity(l.TypeMeta, KindLease, l.Metadata)
}

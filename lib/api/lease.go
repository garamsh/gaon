package api

import "time"

// Lease is a node's liveness: its holder, the time it last renewed and for
// how long. It is named as its Node, and the Node Agent renews it by
// compare-and-swap. Spec carries its current fields; Status is empty until
// the contract gives a Lease one.
type Lease struct {
	TypeMeta
	Metadata ObjectMeta  `json:"metadata"`
	Spec     LeaseSpec   `json:"spec"`
	Status   LeaseStatus `json:"status"`
}

// LeaseList is the answer to listing Leases.
type LeaseList struct {
	TypeMeta
	Metadata ListMeta `json:"metadata"`
	Items    []Lease  `json:"items"`
}

// LeaseStatus is the observed state of a Lease. It has no fields yet.
type LeaseStatus struct{}

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

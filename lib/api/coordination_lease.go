package api

import "time"

// CoordinationLease is a lock any controller can hold, for leader election.
// It is the kind Lease of coordination.gaon.garam.sh, a different kind from
// the Lease of a Node. A candidate takes it, or renews it, by a compare-and-swap
// on the ResourceVersion it read.
type CoordinationLease struct {
	TypeMeta
	Metadata ObjectMeta            `json:"metadata"`
	Spec     CoordinationLeaseSpec `json:"spec"`
}

// CoordinationLeaseList is the answer to listing CoordinationLeases.
type CoordinationLeaseList struct {
	TypeMeta
	Metadata ListMeta            `json:"metadata"`
	Items    []CoordinationLease `json:"items"`
}

// CoordinationLeaseSpec is what the holder writes. An empty Holder is a
// released lease. RenewTime is the holder's own clock reading and is not
// compared with another machine's clock: an observer measures expiry as
// the Lease rule of resource-api.md does.
type CoordinationLeaseSpec struct {
	Holder          string    `json:"holder"`
	RenewTime       time.Time `json:"renewTime"`
	DurationSeconds int32     `json:"durationSeconds"`
}

// Validate checks the identity and the metadata of l, a positive duration,
// and a renew time when there is a holder.
func (l *CoordinationLease) Validate() error {
	if err := validateIdentity(l.TypeMeta, CoordinationGroupVersion, KindCoordinationLease, l.Metadata); err != nil {
		return err
	}
	if err := l.Metadata.Validate(); err != nil {
		return err
	}
	if l.Spec.DurationSeconds <= 0 {
		return invalid("spec.durationSeconds is %d, want positive", l.Spec.DurationSeconds)
	}
	if l.Spec.Holder != "" && l.Spec.RenewTime.IsZero() {
		return invalid("spec.renewTime is empty, want the time the holder renewed")
	}
	return nil
}

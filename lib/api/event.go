package api

import "time"

// Severity says how an Event is to be read.
type Severity string

// The severities of an Event.
const (
	SeverityNormal  Severity = "Normal"
	SeverityWarning Severity = "Warning"
)

// maxEventMessage is the longest message of an Event, in bytes.
const maxEventMessage = 1024

// ClusterEvent is the kind Event: a record that something happened to an
// object. A controller or a Node Agent writes one, and a reader finds the
// Events of an object by its reference. It has no status. The Go name keeps it
// apart from Event, a change in a watch.
type ClusterEvent struct {
	TypeMeta
	Metadata ObjectMeta       `json:"metadata"`
	Spec     ClusterEventSpec `json:"spec"`
}

// ClusterEventList is the answer to listing Events.
type ClusterEventList struct {
	TypeMeta
	Metadata ListMeta       `json:"metadata"`
	Items    []ClusterEvent `json:"items"`
}

// ObjectReference names the object an Event is about. UID is empty when the
// reporter did not know it.
type ObjectReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        UID    `json:"uid,omitempty"`
}

// ClusterEventSpec is what the reporter writes. Reason is a short machine-readable
// word, Message the detail. Count is how many times the same Event happened,
// between FirstTimestamp and LastTimestamp; a reporter that sees it again
// raises Count and moves LastTimestamp instead of writing another Event.
type ClusterEventSpec struct {
	InvolvedObject ObjectReference `json:"involvedObject"`
	Reason         string          `json:"reason"`
	Message        string          `json:"message,omitempty"`
	Type           Severity        `json:"type"`
	Count          int32           `json:"count"`
	FirstTimestamp time.Time       `json:"firstTimestamp"`
	LastTimestamp  time.Time       `json:"lastTimestamp"`
}

// Validate checks the identity and the metadata of e, and its spec: the object
// it names, a reason, a short message, a known type, a positive count and
// timestamps in order.
func (e *ClusterEvent) Validate() error {
	if err := validateIdentity(e.TypeMeta, GroupVersion, KindEvent, e.Metadata); err != nil {
		return err
	}
	if err := e.Metadata.Validate(); err != nil {
		return err
	}
	s := e.Spec
	if o := s.InvolvedObject; o.APIVersion == "" || o.Kind == "" || o.Name == "" {
		return invalid("spec.involvedObject needs apiVersion, kind and name")
	}
	if s.Reason == "" {
		return invalid("spec.reason is empty")
	}
	if len(s.Message) > maxEventMessage {
		return invalid("spec.message is %d bytes, want at most %d", len(s.Message), maxEventMessage)
	}
	if s.Type != SeverityNormal && s.Type != SeverityWarning {
		return invalid("spec.type is %q, want Normal or Warning", s.Type)
	}
	if s.Count < 1 {
		return invalid("spec.count is %d, want at least 1", s.Count)
	}
	if s.FirstTimestamp.IsZero() || s.LastTimestamp.Before(s.FirstTimestamp) {
		return invalid("spec.firstTimestamp is empty or after spec.lastTimestamp")
	}
	return nil
}

// ValidateUpdate checks e as an update of old: Validate, the same object,
// reason and type, and a count that does not fall.
func (e *ClusterEvent) ValidateUpdate(old *ClusterEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Spec.InvolvedObject != old.Spec.InvolvedObject || e.Spec.Reason != old.Spec.Reason || e.Spec.Type != old.Spec.Type {
		return invalid("spec changes the involvedObject, the reason or the type")
	}
	if e.Spec.Count < old.Spec.Count {
		return invalid("spec.count is %d, want at least the stored %d", e.Spec.Count, old.Spec.Count)
	}
	return nil
}

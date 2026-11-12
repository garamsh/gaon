package api

import (
	"encoding/json"
	"fmt"
	"io"
)

// EventType says what a watch event reports.
type EventType string

// The watch event types. A server sends Error once, as the last event of a
// stream.
const (
	EventAdded    EventType = "Added"
	EventModified EventType = "Modified"
	EventDeleted  EventType = "Deleted"
	EventError    EventType = "Error"
)

// WatchEvent is one line of a watch stream, as a server writes it. The object
// is the resource for Added, Modified and Deleted, and an APIError for Error.
type WatchEvent struct {
	Type   EventType       `json:"type"`
	Object json.RawMessage `json:"object"`
}

// NewEvent returns the event of type t, one of Added, Modified and Deleted,
// for obj, a MicroVM, Node, Lease, CertificateSigningRequest, ClusterRole,
// ClusterRoleBinding, CustomResourceDefinition or CustomObject.
func NewEvent[T any](t EventType, obj *T) (WatchEvent, error) {
	switch t {
	case EventAdded, EventModified, EventDeleted:
	default:
		return WatchEvent{}, fmt.Errorf("event type %q does not carry a resource", t)
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return WatchEvent{}, fmt.Errorf("encode %s event: %w", t, err)
	}
	return WatchEvent{Type: t, Object: raw}, nil
}

// NewErrorEvent returns the Error event for e.
func NewErrorEvent(e *APIError) (WatchEvent, error) {
	obj, err := json.Marshal(e)
	if err != nil {
		return WatchEvent{}, fmt.Errorf("encode error event: %w", err)
	}
	return WatchEvent{Type: EventError, Object: obj}, nil
}

// Event is a change to a resource, decoded from a watch.
type Event[T any] struct {
	Type   EventType
	Object T
}

// Watch reads the events of a watch of MicroVMs, Nodes, Leases,
// CertificateSigningRequests, ClusterRoles, ClusterRoleBindings,
// CustomResourceDefinitions or CustomObjects.
type Watch[T any] struct {
	kind string
	body io.Closer
	dec  *json.Decoder
}

func newWatch[T any](kind string, body io.ReadCloser) *Watch[T] {
	return &Watch[T]{kind: kind, body: body, dec: json.NewDecoder(body)}
}

// Next returns the next event. It returns io.EOF when the stream ends, and
// the *APIError of an Error event, which matches ErrExpired when the version
// the watch resumed from has expired.
func (w *Watch[T]) Next() (Event[T], error) {
	var ev WatchEvent
	if err := w.dec.Decode(&ev); err != nil {
		return Event[T]{}, err
	}
	switch ev.Type {
	case EventAdded, EventModified, EventDeleted:
		var probe TypeMeta
		if err := json.Unmarshal(ev.Object, &probe); err != nil {
			return Event[T]{}, fmt.Errorf("decode %s event: %w", ev.Type, err)
		}
		if probe.Kind != w.kind {
			return Event[T]{}, fmt.Errorf("%s event carries kind %q, want %q", ev.Type, probe.Kind, w.kind)
		}
		var obj T
		if err := json.Unmarshal(ev.Object, &obj); err != nil {
			return Event[T]{}, fmt.Errorf("decode %s event: %w", ev.Type, err)
		}
		return Event[T]{Type: ev.Type, Object: obj}, nil
	case EventError:
		var e APIError
		if err := json.Unmarshal(ev.Object, &e); err != nil {
			return Event[T]{}, fmt.Errorf("decode Error event: %w", err)
		}
		return Event[T]{}, &e
	default:
		return Event[T]{}, fmt.Errorf("unknown event type %q", ev.Type)
	}
}

// Close ends the watch.
func (w *Watch[T]) Close() error {
	return w.body.Close()
}

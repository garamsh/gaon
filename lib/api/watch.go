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
// is the Machine for Added, Modified and Deleted, and an APIError for Error.
type WatchEvent struct {
	Type   EventType       `json:"type"`
	Object json.RawMessage `json:"object"`
}

// NewMachineEvent returns the event of type t, one of Added, Modified and
// Deleted, for m.
func NewMachineEvent(t EventType, m *Machine) (WatchEvent, error) {
	switch t {
	case EventAdded, EventModified, EventDeleted:
	default:
		return WatchEvent{}, fmt.Errorf("event type %q does not carry a machine", t)
	}
	obj, err := json.Marshal(m)
	if err != nil {
		return WatchEvent{}, fmt.Errorf("encode machine event: %w", err)
	}
	return WatchEvent{Type: t, Object: obj}, nil
}

// NewErrorEvent returns the Error event for e.
func NewErrorEvent(e *APIError) (WatchEvent, error) {
	obj, err := json.Marshal(e)
	if err != nil {
		return WatchEvent{}, fmt.Errorf("encode error event: %w", err)
	}
	return WatchEvent{Type: EventError, Object: obj}, nil
}

// MachineEvent is a change to a Machine, decoded from a watch.
type MachineEvent struct {
	Type    EventType
	Machine Machine
}

// MachineWatch reads the events of a watch of Machines.
type MachineWatch struct {
	body io.Closer
	dec  *json.Decoder
}

func newMachineWatch(body io.ReadCloser) *MachineWatch {
	return &MachineWatch{body: body, dec: json.NewDecoder(body)}
}

// Next returns the next event. It returns io.EOF when the stream ends, and
// the *APIError of an Error event, which matches ErrExpired when the version
// the watch resumed from has expired.
func (w *MachineWatch) Next() (MachineEvent, error) {
	var ev WatchEvent
	if err := w.dec.Decode(&ev); err != nil {
		return MachineEvent{}, err
	}
	switch ev.Type {
	case EventAdded, EventModified, EventDeleted:
		var m Machine
		if err := json.Unmarshal(ev.Object, &m); err != nil {
			return MachineEvent{}, fmt.Errorf("decode %s event: %w", ev.Type, err)
		}
		if m.Kind != KindMachine {
			return MachineEvent{}, fmt.Errorf("%s event carries kind %q, want %q", ev.Type, m.Kind, KindMachine)
		}
		return MachineEvent{Type: ev.Type, Machine: m}, nil
	case EventError:
		var e APIError
		if err := json.Unmarshal(ev.Object, &e); err != nil {
			return MachineEvent{}, fmt.Errorf("decode Error event: %w", err)
		}
		return MachineEvent{}, &e
	default:
		return MachineEvent{}, fmt.Errorf("unknown event type %q", ev.Type)
	}
}

// Close ends the watch.
func (w *MachineWatch) Close() error {
	return w.body.Close()
}

// Package node registers the node with the resource API and keeps its Node
// status current.
package node

import (
	"errors"
	"strconv"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// ErrNoCapacity is returned when mvmd's runtime info carries no capacity.
var ErrNoCapacity = errors.New("runtime info carries no capacity")

// capacity converts the capacity mvmd reports into the Node's quantities:
// whole cpus, and bytes for memory and storage.
func capacity(c *v1alpha2.NodeCapacity) (api.NodeCapacity, error) {
	if c == nil {
		return api.NodeCapacity{}, ErrNoCapacity
	}
	return api.NodeCapacity{
		CPU:     api.Quantity(strconv.FormatUint(uint64(c.GetCpus()), 10)),
		Memory:  api.Quantity(strconv.FormatUint(c.GetMemoryBytes(), 10)),
		Storage: api.Quantity(strconv.FormatUint(c.GetStateTotalBytes(), 10)),
	}, nil
}

// Reasons of the Ready condition.
const (
	ReasonRuntimeReachable   = "RuntimeReachable"
	ReasonRuntimeUnreachable = "RuntimeUnreachable"
)

// withReady returns conditions with Ready set to status, reason and message.
// The transition time of a Ready that already has the same status and reason
// is kept; every other condition is kept as it is.
func withReady(conditions []api.Condition, status api.ConditionStatus, reason, message string, now time.Time) []api.Condition {
	t := now.UTC()
	out := make([]api.Condition, 0, len(conditions)+1)
	for _, c := range conditions {
		if c.Type != api.NodeReady {
			out = append(out, c)
			continue
		}
		if c.Status == status && c.Reason == reason && c.LastTransitionTime != nil {
			t = *c.LastTransitionTime
		}
	}
	return append(out, api.Condition{Type: api.NodeReady, Status: status, Reason: reason, Message: message, LastTransitionTime: &t})
}

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

// withReady returns conditions with Ready true, keeping the transition time
// of a Ready that already is true and every other condition as it is.
func withReady(conditions []api.Condition, now time.Time) []api.Condition {
	out := make([]api.Condition, 0, len(conditions)+1)
	for _, c := range conditions {
		if c.Type == api.NodeReady && c.Status == api.ConditionTrue {
			return append(out, conditions...)
		}
		if c.Type != api.NodeReady {
			out = append(out, c)
		}
	}
	t := now.UTC()
	return append(out, api.Condition{Type: api.NodeReady, Status: api.ConditionTrue, Reason: "RuntimeReachable", LastTransitionTime: &t})
}

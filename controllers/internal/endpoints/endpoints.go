// Package endpoints writes the Endpoints of each Service that has a selector
// from the Ready MicroVMs the selector selects.
package endpoints

import (
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// RetryDelay is how long the controller waits before it retries a failed
// write or a failed list or watch.
const RetryDelay = 5 * time.Second

// readyCondition is the MicroVM condition the Node Agent writes when the
// readiness probe passes.
const readyCondition = "Ready"

// listed reports whether m belongs in an Endpoints: it has no
// deletionTimestamp and its Ready condition is True. The address is checked
// where it is picked.
func listed(m *api.MicroVM) bool {
	if m.Metadata.DeletionTimestamp != nil {
		return false
	}
	for _, c := range m.Status.Conditions {
		if c.Type == readyCondition {
			return c.Status == api.ConditionTrue
		}
	}
	return false
}

// selects reports whether every pair of selector is a label of labels. An
// empty selector selects none.
func selects(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

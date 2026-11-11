package scheduler

import (
	"fmt"
	"math/big"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// resources is an amount of cpu and memory.
type resources struct {
	cpu, memory *big.Rat
}

func parseResources(cpu, memory api.Quantity) (resources, error) {
	c, err := cpu.Value()
	if err != nil {
		return resources{}, fmt.Errorf("cpu: %w", err)
	}
	m, err := memory.Value()
	if err != nil {
		return resources{}, fmt.Errorf("memory: %w", err)
	}
	return resources{cpu: c, memory: m}, nil
}

func requests(m *api.Machine) (resources, error) {
	r := m.Spec.Resources.Requests
	res, err := parseResources(r.CPU, r.Memory)
	if err != nil {
		return resources{}, fmt.Errorf("machine %s requests: %w", m.Metadata.Name, err)
	}
	return res, nil
}

func capacity(n *api.Node) (resources, error) {
	c := n.Status.Capacity
	res, err := parseResources(c.CPU, c.Memory)
	if err != nil {
		return resources{}, fmt.Errorf("node %s capacity: %w", n.Metadata.Name, err)
	}
	return res, nil
}

func (r resources) minus(o resources) resources {
	return resources{cpu: new(big.Rat).Sub(r.cpu, o.cpu), memory: new(big.Rat).Sub(r.memory, o.memory)}
}

func (r resources) covers(o resources) bool {
	return r.cpu.Cmp(o.cpu) >= 0 && r.memory.Cmp(o.memory) >= 0
}

// eligible reports whether the Scheduler may place a Machine on n: it is
// Admitted, Ready, and the node lifecycle controller has not set LeaseExpired.
func eligible(n *api.Node) bool {
	return n.Status.State == api.NodeStateAdmitted &&
		condition(n, api.NodeReady) == api.ConditionTrue &&
		condition(n, api.NodeLeaseExpired) != api.ConditionTrue
}

// leaseExpired reports whether the node lifecycle controller has confirmed
// the expiry of n's Lease.
func leaseExpired(n *api.Node) bool {
	return condition(n, api.NodeLeaseExpired) == api.ConditionTrue
}

// condition returns the status of the condition of type t, empty when n has none.
func condition(n *api.Node, t string) api.ConditionStatus {
	for _, c := range n.Status.Conditions {
		if c.Type == t {
			return c.Status
		}
	}
	return ""
}

// ledger holds the free capacity of every eligible Node.
type ledger struct {
	nodes int
	names []string
	free  map[string]resources
}

// newLedger takes the capacity of each eligible Node less the requests of the
// Machines bound to it, whatever their phase.
func newLedger(nodes []api.Node, machines []api.Machine) (*ledger, error) {
	l := &ledger{nodes: len(nodes), free: map[string]resources{}}
	for i := range nodes {
		n := &nodes[i]
		if !eligible(n) {
			continue
		}
		c, err := capacity(n)
		if err != nil {
			return nil, err
		}
		l.names = append(l.names, n.Metadata.Name)
		l.free[n.Metadata.Name] = c
	}
	slices.Sort(l.names)
	for i := range machines {
		m := &machines[i]
		free, ok := l.free[m.Spec.NodeName]
		if !ok {
			continue
		}
		r, err := requests(m)
		if err != nil {
			return nil, err
		}
		l.free[m.Spec.NodeName] = free.minus(r)
	}
	return l, nil
}

// choose returns the first eligible Node, by name, whose free capacity covers r.
func (l *ledger) choose(r resources) (string, bool) {
	for _, name := range l.names {
		if l.free[name].covers(r) {
			return name, true
		}
	}
	return "", false
}

// take charges r to the named Node.
func (l *ledger) take(name string, r resources) {
	l.free[name] = l.free[name].minus(r)
}

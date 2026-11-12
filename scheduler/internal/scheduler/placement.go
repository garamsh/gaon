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

// nodeView is a Node as the plugins see it. capacity and free are set only
// for an eligible Node.
type nodeView struct {
	node           *api.Node
	capacity, free resources
}

// ledger holds the Nodes of a pass and the free capacity of each eligible one.
type ledger struct {
	names    []string // every Node, sorted
	eligible int
	views    map[string]*nodeView
}

// newLedger takes the capacity of each eligible Node less the requests of the
// Machines bound to it, whatever their phase.
func newLedger(nodes []api.Node, machines []api.Machine) (*ledger, error) {
	l := &ledger{views: map[string]*nodeView{}}
	for i := range nodes {
		n := &nodes[i]
		v := &nodeView{node: n}
		if eligible(n) {
			c, err := capacity(n)
			if err != nil {
				return nil, err
			}
			v.capacity, v.free = c, c
			l.eligible++
		}
		l.names = append(l.names, n.Metadata.Name)
		l.views[n.Metadata.Name] = v
	}
	slices.Sort(l.names)
	for i := range machines {
		m := &machines[i]
		v, ok := l.views[m.Spec.NodeName]
		if !ok || !eligible(v.node) {
			continue
		}
		r, err := requests(m)
		if err != nil {
			return nil, err
		}
		v.free = v.free.minus(r)
	}
	return l, nil
}

// take charges r to the named Node.
func (l *ledger) take(name string, r resources) {
	v := l.views[name]
	v.free = v.free.minus(r)
}

package scheduler

import (
	"fmt"
	"math/big"
)

// Names of the built-in plugins.
const (
	PluginNodeEligible     = "NodeEligible"
	PluginNodeResourcesFit = "NodeResourcesFit"
	PluginVolumeNode       = "VolumeNode"
	PluginLeastAllocated   = "LeastAllocated"
)

const (
	maxScore               = 100
	reasonNotEligible      = "Node is not Admitted, Ready and not LeaseExpired"
	reasonInsufficientFree = "insufficient free cpu or memory"
)

// demand is what a MicroVM asks of the Node it goes to.
type demand struct {
	req     resources
	volumes volumeNeed
}

// filterPlugin decides whether a MicroVM can go to a Node.
type filterPlugin interface {
	Name() string
	// Filter returns the reason a MicroVM with demand d does not fit n, or
	// the empty string when it fits.
	Filter(d demand, n *nodeView) string
}

// scorePlugin ranks a Node that passed every filter.
type scorePlugin interface {
	Name() string
	// Score returns 0 to maxScore for placing a MicroVM requesting req on n.
	Score(req resources, n *nodeView) int
}

// filterPlugins always run, in this order. NodeEligible comes first because
// NodeResourcesFit reads the capacity only an eligible Node has.
var filterPlugins = []filterPlugin{nodeEligible{}, nodeResourcesFit{}, volumeNode{}}

// scorePlugins are the ones a configuration may enable, by name.
var scorePlugins = map[string]scorePlugin{PluginLeastAllocated: leastAllocated{}}

type nodeEligible struct{}

func (nodeEligible) Name() string { return PluginNodeEligible }

func (nodeEligible) Filter(_ demand, n *nodeView) string {
	if eligible(n.node) {
		return ""
	}
	return reasonNotEligible
}

type nodeResourcesFit struct{}

func (nodeResourcesFit) Name() string { return PluginNodeResourcesFit }

func (nodeResourcesFit) Filter(d demand, n *nodeView) string {
	if n.free.covers(d.req) {
		return ""
	}
	return reasonInsufficientFree
}

// volumeNode keeps only the Node the MicroVM's pinned local Volumes name.
type volumeNode struct{}

func (volumeNode) Name() string { return PluginVolumeNode }

func (volumeNode) Filter(d demand, n *nodeView) string {
	switch {
	case d.volumes.refusal != "":
		return d.volumes.refusal
	case d.volumes.node != "" && d.volumes.node != n.node.Metadata.Name:
		return fmt.Sprintf("local volumes are pinned to Node %q", d.volumes.node)
	}
	return ""
}

// leastAllocated scores the share of cpu and memory left free after the
// MicroVM, each rounded down to a whole number from 0 to maxScore and
// averaged with the same rounding. A resource the Node has none of scores 0.
type leastAllocated struct{}

func (leastAllocated) Name() string { return PluginLeastAllocated }

func (leastAllocated) Score(req resources, n *nodeView) int {
	left := n.free.minus(req)
	return (freeShare(left.cpu, n.capacity.cpu) + freeShare(left.memory, n.capacity.memory)) / 2
}

func freeShare(left, capacity *big.Rat) int {
	if capacity.Sign() <= 0 || left.Sign() < 0 {
		return 0
	}
	share := new(big.Rat).Mul(left, big.NewRat(maxScore, 1))
	share.Quo(share, capacity)
	return int(new(big.Int).Quo(share.Num(), share.Denom()).Int64())
}

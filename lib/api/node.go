package api

// NodeStateDiscovered is the pre-registration state of a Node: a machine naru
// has discovered and gaon has not admitted.
const NodeStateDiscovered NodeState = "Discovered"

// NodeState is the state a Node's status reports. Discovered is the only one
// the contract names.
type NodeState string

// Node is a physical machine.
type Node struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Status   NodeStatus `json:"status"`
}

// NodeStatus is the observed state of a Node.
type NodeStatus struct {
	State NodeState `json:"state,omitempty"`
}

// Validate checks the identity of n.
func (n *Node) Validate() error {
	return validateIdentity(n.TypeMeta, KindNode, n.Metadata)
}

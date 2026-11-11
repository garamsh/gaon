package api

// NodeStateDiscovered is the pre-registration state of a Node: a machine naru
// has discovered and gaon has not admitted.
const NodeStateDiscovered NodeState = "Discovered"

// NodeState is the state a Node's status reports. Discovered is the only one
// the contract names.
type NodeState string

// Node is a physical machine. Status carries its current fields; Spec is empty
// until the contract gives a Node one.
type Node struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

// NodeSpec is the desired state of a Node. It has no fields yet.
type NodeSpec struct{}

// NodeStatus is the observed state of a Node.
type NodeStatus struct {
	State NodeState `json:"state,omitempty"`
}

// Validate checks the identity of n.
func (n *Node) Validate() error {
	return validateIdentity(n.TypeMeta, KindNode, n.Metadata)
}

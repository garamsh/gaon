package api

import "net/netip"

// NodeState is the state a Node's status reports.
type NodeState string

// The Node states: Discovered until a user sets spec.admitted, Admitted after.
const (
	NodeStateDiscovered NodeState = "Discovered"
	NodeStateAdmitted   NodeState = "Admitted"
)

// The condition types of a Node. The Node Agent writes Ready, and the node
// lifecycle controller writes LeaseExpired.
const (
	NodeReady        = "Ready"
	NodeLeaseExpired = "LeaseExpired"
)

// NodeGroup is the Organization of a Node Agent's client certificate: the
// signer requires it in a Node's request, and a verifier outside gaon reads it
// to tell a Node from any other identity the cluster CA signs.
const NodeGroup = "gaon:nodes"

// Node is a physical machine.
type Node struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

// NodeList is the answer to listing Nodes.
type NodeList struct {
	TypeMeta
	Metadata ListMeta `json:"metadata"`
	Items    []Node   `json:"items"`
}

// NodeSpec is the desired state of a Node. A user admits the Node by setting
// Admitted; the Node Agent creates it with Admitted false.
type NodeSpec struct {
	Admitted bool `json:"admitted"`
}

// NodeCapacity is what the Node offers, from mvmd's runtime info.
type NodeCapacity struct {
	CPU     Quantity `json:"cpu,omitempty"`
	Memory  Quantity `json:"memory,omitempty"`
	Storage Quantity `json:"storage,omitempty"`
}

// NodeStatus is the observed state of a Node.
type NodeStatus struct {
	State     NodeState    `json:"state,omitempty"`
	Capacity  NodeCapacity `json:"capacity"`
	Addresses []string     `json:"addresses,omitempty"`
	// AgentPort is the port the Node Agent serves a MicroVM's log and exec
	// on, at an address of Addresses (ADR 0043). Zero means it serves none.
	AgentPort int `json:"agentPort,omitempty"`
	// MicroVMPrefixes are the CIDR prefixes the Node's MicroVMs are addressed
	// from, as mvmd reports them.
	MicroVMPrefixes []string    `json:"microvmPrefixes,omitempty"`
	Conditions      []Condition `json:"conditions,omitempty"`
}

// Validate checks the identity of n.
func (n *Node) Validate() error {
	return validateIdentity(n.TypeMeta, GroupVersion, KindNode, n.Metadata)
}

// ValidateStatus checks that n's status names a known state, that each of its
// MicroVM prefixes is a CIDR, that its agent port is a port and that each of
// its conditions has a known status.
func (n *Node) ValidateStatus() error {
	switch n.Status.State {
	case NodeStateDiscovered, NodeStateAdmitted:
	default:
		return invalid("status.state is %q, want Discovered or Admitted", n.Status.State)
	}
	for _, p := range n.Status.MicroVMPrefixes {
		if _, err := netip.ParsePrefix(p); err != nil {
			return invalid("status.microvmPrefixes has %q, which is not a CIDR", p)
		}
	}
	if n.Status.AgentPort < 0 || n.Status.AgentPort > 65535 {
		return invalid("status.agentPort is %d, want 0 (none) to 65535", n.Status.AgentPort)
	}
	for _, c := range n.Status.Conditions {
		if err := c.validate(); err != nil {
			return err
		}
	}
	return nil
}

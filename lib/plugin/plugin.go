// Package plugin is the gaon plugin API: how the Node Agent reaches a network
// plugin and a storage plugin, each a process of its own that serves the gRPC
// services of gaon.plugin.v1alpha1 on a Unix socket in the plugin directory.
//
// A plugin implements Network or Storage and calls ServeNetwork or
// ServeStorage. The agent calls DialNetwork or DialStorage and uses the
// result as the same interface. The meaning of each call is in
// docs/architecture/node-agent.md §Plugins.
package plugin

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// Network decides the network policy a MicroVM is created with.
type Network interface {
	// Policy returns the policy m is created with. It returns a *Refusal for
	// a MicroVM this network does not serve; any other error is retried.
	Policy(ctx context.Context, m *api.MicroVM) (*NetworkPolicy, error)
}

// Storage readies a MicroVM's volumes on the node.
type Storage interface {
	// Attach readies the volumes of m and returns the disks m is created
	// with. It returns a *Refusal for a MicroVM whose volumes it cannot
	// serve; any other error is retried, so Attach is repeatable for the same
	// MicroVM and epoch.
	Attach(ctx context.Context, m *api.MicroVM) ([]DiskAttachment, error)
	// Release gives back what Attach readied for the mvmd machine id. It is
	// repeatable, and called for an id Attach never saw.
	Release(ctx context.Context, id string) error
}

// Refusal is a plugin's verdict that it does not serve a MicroVM. The MicroVM
// fails with Reason in its Ready condition and Message as the condition's
// message.
type Refusal struct {
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// Protocol is the protocol a NetworkRule matches.
type Protocol int

// The protocols of a rule.
const (
	TCP Protocol = iota + 1
	UDP
	ICMP
	ICMPv6
	Any
)

// PortRange is the ports First to Last, both included.
type PortRange struct {
	First, Last uint32
}

// NetworkRule is one entry of a policy's egress or ingress list. Prefix is an
// IPv4 or IPv6 CIDR. No Ports means every port, and only a TCP or UDP rule may
// set them.
type NetworkRule struct {
	Prefix   string
	Protocol Protocol
	Ports    []PortRange
}

// NetworkPolicy is what a MicroVM is created with. The empty policy blocks all
// traffic to and from outside the node.
type NetworkPolicy struct {
	Egress  []NetworkRule
	Ingress []NetworkRule
}

// DiskAttachment is one disk the node holds, attached to a MicroVM.
type DiskAttachment struct {
	DiskID   string
	ReadOnly bool
}

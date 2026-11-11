package node

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// Nodes is the part of the resource API the registration uses.
type Nodes interface {
	GetNode(ctx context.Context, name string) (*api.Node, error)
	PutNode(ctx context.Context, n *api.Node) (*api.Node, error)
	PutNodeStatus(ctx context.Context, n *api.Node) (*api.Node, error)
}

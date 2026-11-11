package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutNode creates n with state Discovered, or changes the spec of the stored
// Node n was read at. Neither takes the status from n.
func (s *service) PutNode(ctx context.Context, name string, n *api.Node) (*api.Node, error) {
	if err := checkName(name, n.Metadata.Name); err != nil {
		return nil, err
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}
	return nodes.put(ctx, s.st, n, func() error {
		n.Status = api.NodeStatus{State: api.NodeStateDiscovered}
		return nil
	}, func(old *api.Node) error {
		n.Status = old.Status
		return nil
	})
}

// PutNodeStatus replaces the status of the stored Node n was read at, if its
// state agrees with the stored spec.admitted.
func (s *service) PutNodeStatus(ctx context.Context, name string, next *api.Node) (*api.Node, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	n, rev, err := nodes.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(nodes.plural, name, next.Metadata.ResourceVersion, n.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := next.ValidateStatus(); err != nil {
		return nil, err
	}
	if want := stateFor(n.Spec.Admitted); next.Status.State != n.Status.State && next.Status.State != want {
		return nil, api.NewAPIError(api.ReasonInvalid, "status.state is %q, want %q while spec.admitted is %t", next.Status.State, want, n.Spec.Admitted)
	}
	n.Status = next.Status
	return n, nodes.update(ctx, s.st, n, rev)
}

func stateFor(admitted bool) api.NodeState {
	if admitted {
		return api.NodeStateAdmitted
	}
	return api.NodeStateDiscovered
}

package machines

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Network is the plugin that decides the network policy a Machine is created
// with. The agent calls it before CreateMachine and puts the result in that
// request.
type Network interface {
	// Policy returns the policy m is created with. It returns a *Refusal for
	// a Machine this network does not serve; any other error is retried.
	Policy(ctx context.Context, m *api.Machine) (*v1alpha2.NetworkPolicy, error)
}

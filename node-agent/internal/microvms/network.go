package microvms

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Network is the plugin that decides the network policy a MicroVM is created
// with. The agent calls it before CreateMicroVM and puts the result in that
// request.
type Network interface {
	// Policy returns the policy m is created with. It returns a *Refusal for
	// a MicroVM this network does not serve; any other error is retried.
	Policy(ctx context.Context, m *api.MicroVM) (*v1alpha2.NetworkPolicy, error)
}

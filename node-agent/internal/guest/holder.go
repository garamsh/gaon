package guest

import "github.com/garamsh/gaon/lib/api"

// Holder says which MicroVMs the agent holds: the loop that runs them
// implements it.
type Holder interface {
	// Holds returns the MicroVM named when it is bound to this node at epoch
	// and has uid.
	Holds(name string, epoch api.Epoch, uid api.UID) (api.MicroVM, bool)
}

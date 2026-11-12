package microvms

import "github.com/garamsh/gaon/lib/plugin"

// Storage is the plugin that readies a MicroVM's volumes on the node. The
// agent calls Attach before CreateMicroVM and puts the disks in that request,
// and Release after it removes the MicroVM from mvmd. In production it is the
// client of a plugin process.
type Storage = plugin.Storage

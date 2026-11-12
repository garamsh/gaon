package microvms

import "github.com/garamsh/gaon/lib/plugin"

// Network is the plugin that decides the network policy a MicroVM is created
// with. The agent calls it before CreateMicroVM and puts the result in that
// request. In production it is the client of a plugin process.
type Network = plugin.Network

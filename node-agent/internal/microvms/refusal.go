package microvms

import "github.com/garamsh/gaon/lib/plugin"

// Refusal is a plugin's refusal of a MicroVM. The MicroVM fails with Reason
// in its Ready condition and Message as the condition's message.
type Refusal = plugin.Refusal

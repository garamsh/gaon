package guest

import "github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"

// Runtime is mvmd's runtime API as the guest service uses it. It is the whole
// client because ReadExecOutput takes one.
type Runtime = v1alpha2.RuntimeServiceClient

package machines

import "github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"

// Runtime is mvmd's runtime API as the Machines use it. It is the whole
// client because ReadExecOutput takes one.
type Runtime = v1alpha2.RuntimeServiceClient

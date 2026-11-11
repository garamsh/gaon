package node

import (
	"context"

	"google.golang.org/grpc"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Runtime is the part of mvmd's runtime API the registration uses.
type Runtime interface {
	GetRuntimeInfo(ctx context.Context, in *v1alpha2.GetRuntimeInfoRequest, opts ...grpc.CallOption) (*v1alpha2.GetRuntimeInfoResponse, error)
}

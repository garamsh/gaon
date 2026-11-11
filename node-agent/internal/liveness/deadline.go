package liveness

import (
	"context"

	"google.golang.org/grpc"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Deadline is the part of mvmd's runtime API the renewal uses.
type Deadline interface {
	RenewDeadline(ctx context.Context, in *v1alpha2.RenewDeadlineRequest, opts ...grpc.CallOption) (*v1alpha2.RenewDeadlineResponse, error)
	InspectDeadline(ctx context.Context, in *v1alpha2.InspectDeadlineRequest, opts ...grpc.CallOption) (*v1alpha2.InspectDeadlineResponse, error)
}

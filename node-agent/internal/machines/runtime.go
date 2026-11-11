package machines

import (
	"context"

	"google.golang.org/grpc"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Runtime is the part of mvmd's runtime API the Machines use.
type Runtime interface {
	PullImage(ctx context.Context, in *v1alpha2.PullImageRequest, opts ...grpc.CallOption) (*v1alpha2.PullImageResponse, error)
	CreateMachine(ctx context.Context, in *v1alpha2.CreateMachineRequest, opts ...grpc.CallOption) (*v1alpha2.CreateMachineResponse, error)
	StartMachine(ctx context.Context, in *v1alpha2.StartMachineRequest, opts ...grpc.CallOption) (*v1alpha2.StartMachineResponse, error)
	StopMachine(ctx context.Context, in *v1alpha2.StopMachineRequest, opts ...grpc.CallOption) (*v1alpha2.StopMachineResponse, error)
	DeleteMachine(ctx context.Context, in *v1alpha2.DeleteMachineRequest, opts ...grpc.CallOption) (*v1alpha2.DeleteMachineResponse, error)
	InspectMachine(ctx context.Context, in *v1alpha2.InspectMachineRequest, opts ...grpc.CallOption) (*v1alpha2.InspectMachineResponse, error)
	ListMachines(ctx context.Context, in *v1alpha2.ListMachinesRequest, opts ...grpc.CallOption) (*v1alpha2.ListMachinesResponse, error)
}

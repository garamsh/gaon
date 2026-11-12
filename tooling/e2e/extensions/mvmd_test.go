//go:build extensions

package extensions

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// A Machine whose id starts with exitsPrefix stops as soon as it is started, as
// an entrypoint that ran and returned 0 does; one that starts with failsPrefix
// stops the same way with exit code 1. Every other Machine keeps running.
const (
	exitsPrefix = "exits-"
	failsPrefix = "fails-"
)

// mvmd is the smallest mvmd a Node Agent runs against: it serves the runtime
// API on a Unix socket, reports the capacity it was given, and starts every
// Machine it is asked to create. It runs nothing.
type mvmd struct {
	v1alpha2.UnimplementedRuntimeServiceServer

	socket   string
	capacity *v1alpha2.NodeCapacity

	mu       sync.Mutex
	machines map[string]*v1alpha2.Machine
	renewal  uint64
	length   time.Duration
	expiry   time.Time
}

// startMvmd serves a runtime API with cpus and memoryBytes of capacity on a
// socket in dir, and stops it when the test ends. A socket path is limited to
// about 100 bytes, so dir must be short.
func startMvmd(t *testing.T, dir, node string, cpus uint32, memoryBytes uint64) *mvmd {
	t.Helper()
	m := &mvmd{
		socket:   dir + "/" + runTag + "-" + node + ".sock",
		capacity: &v1alpha2.NodeCapacity{Cpus: cpus, MemoryBytes: memoryBytes, StateTotalBytes: 100 << 30, StateAvailableBytes: 90 << 30},
		machines: map[string]*v1alpha2.Machine{},
	}
	require.Less(t, len(m.socket), 100, "the socket path is too long for a Unix socket")
	lis, err := net.Listen("unix", m.socket)
	require.NoError(t, err)
	g := grpc.NewServer()
	v1alpha2.RegisterRuntimeServiceServer(g, m)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return m
}

func (m *mvmd) GetRuntimeInfo(context.Context, *v1alpha2.GetRuntimeInfoRequest) (*v1alpha2.GetRuntimeInfoResponse, error) {
	return &v1alpha2.GetRuntimeInfoResponse{ApiVersions: []string{"mvmd.runtime.v1alpha2"}, Capacity: m.capacity}, nil
}

func (m *mvmd) PullImage(context.Context, *v1alpha2.PullImageRequest) (*v1alpha2.PullImageResponse, error) {
	return &v1alpha2.PullImageResponse{}, nil
}

func (m *mvmd) CreateMachine(_ context.Context, r *v1alpha2.CreateMachineRequest) (*v1alpha2.CreateMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.machines[r.GetMachineId()]; ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_CONFLICT, "machine exists")
	}
	created := &v1alpha2.Machine{
		Id:        r.GetMachineId(),
		State:     v1alpha2.MachineState_MACHINE_STATE_CREATED,
		Resources: r.GetResources(),
		Addresses: []string{"10.0.0.2"},
		Disks:     r.GetDisks(),
	}
	m.machines[created.Id] = created
	return &v1alpha2.CreateMachineResponse{Machine: proto.Clone(created).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) StartMachine(_ context.Context, r *v1alpha2.StartMachineRequest) (*v1alpha2.StartMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	held.State = v1alpha2.MachineState_MACHINE_STATE_RUNNING
	if exitCode(held.Id) >= 0 {
		held.State = v1alpha2.MachineState_MACHINE_STATE_STOPPED
	}
	return &v1alpha2.StartMachineResponse{Machine: proto.Clone(held).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) StopMachine(_ context.Context, r *v1alpha2.StopMachineRequest) (*v1alpha2.StopMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	held.State = v1alpha2.MachineState_MACHINE_STATE_STOPPED
	return &v1alpha2.StopMachineResponse{Machine: proto.Clone(held).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) DeleteMachine(_ context.Context, r *v1alpha2.DeleteMachineRequest) (*v1alpha2.DeleteMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.machines, r.GetMachineId())
	return &v1alpha2.DeleteMachineResponse{}, nil
}

func (m *mvmd) InspectMachine(_ context.Context, r *v1alpha2.InspectMachineRequest) (*v1alpha2.InspectMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	return &v1alpha2.InspectMachineResponse{Machine: proto.Clone(held).(*v1alpha2.Machine)}, nil
}

// exitCode is the code the entrypoint of the Machine id exits with, or -1 for a
// Machine that does not exit.
func exitCode(id string) int32 {
	switch {
	case strings.HasPrefix(id, exitsPrefix):
		return 0
	case strings.HasPrefix(id, failsPrefix):
		return 1
	}
	return -1
}

// ReadExecOutput answers a read of the entrypoint of a Machine that exits with
// its record.
func (m *mvmd) ReadExecOutput(r *v1alpha2.ReadExecOutputRequest, stream grpc.ServerStreamingServer[v1alpha2.ReadExecOutputResponse]) error {
	m.mu.Lock()
	_, held := m.machines[r.GetMachineId()]
	m.mu.Unlock()
	code := exitCode(r.GetMachineId())
	if !held || code < 0 || r.GetExecId() != "entrypoint" {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such exec")
	}
	rec := &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED, ExitCode: code}
	return stream.Send(&v1alpha2.ReadExecOutputResponse{Part: &v1alpha2.ReadExecOutputResponse_Record{Record: rec}})
}

func (m *mvmd) ListMachines(context.Context, *v1alpha2.ListMachinesRequest) (*v1alpha2.ListMachinesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &v1alpha2.ListMachinesResponse{}
	for _, held := range m.machines {
		out.Machines = append(out.Machines, proto.Clone(held).(*v1alpha2.Machine))
	}
	return out, nil
}

// WatchEvents holds the stream open and sends nothing: no Machine changes
// state by itself here.
func (m *mvmd) WatchEvents(_ *v1alpha2.WatchEventsRequest, stream v1alpha2.RuntimeService_WatchEventsServer) error {
	<-stream.Context().Done()
	return nil
}

func (m *mvmd) RenewDeadline(_ context.Context, r *v1alpha2.RenewDeadlineRequest) (*v1alpha2.RenewDeadlineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.GetRenewalNumber() > m.renewal {
		m.renewal = r.GetRenewalNumber()
		m.length = r.GetLength().AsDuration()
		m.expiry = time.Now().Add(m.length)
	}
	return &v1alpha2.RenewDeadlineResponse{Status: m.deadlineStatus()}, nil
}

func (m *mvmd) InspectDeadline(context.Context, *v1alpha2.InspectDeadlineRequest) (*v1alpha2.InspectDeadlineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &v1alpha2.InspectDeadlineResponse{Status: m.deadlineStatus()}, nil
}

func (m *mvmd) deadlineStatus() *v1alpha2.DeadlineStatus {
	st := &v1alpha2.DeadlineStatus{Phase: v1alpha2.DeadlinePhase_DEADLINE_PHASE_NOT_ARMED, LastRenewalNumber: m.renewal}
	if m.renewal == 0 {
		return st
	}
	st.Length = durationpb.New(m.length)
	st.Phase = v1alpha2.DeadlinePhase_DEADLINE_PHASE_ARMED
	if remaining := time.Until(m.expiry); remaining > 0 {
		st.Remaining = durationpb.New(remaining)
	} else {
		st.Phase = v1alpha2.DeadlinePhase_DEADLINE_PHASE_EXPIRED
	}
	return st
}

package fakemvmd

import (
	"context"
	"slices"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

func (s *Server) record(call, id string) {
	s.calls = append(s.calls, call+" "+id)
}

func (s *Server) GetRuntimeInfo(context.Context, *v1alpha2.GetRuntimeInfoRequest) (*v1alpha2.GetRuntimeInfoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &v1alpha2.GetRuntimeInfoResponse{
		ApiVersions:     []string{"mvmd.runtime.v1alpha2"},
		Capacity:        s.capacity,
		MachinePrefixes: slices.Clone(s.prefixes),
	}, nil
}

func (s *Server) PullImage(_ context.Context, r *v1alpha2.PullImageRequest) (*v1alpha2.PullImageResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("PullImage", r.GetImage().GetDigest())
	s.images = append(s.images, r.GetImage().GetDigest())
	return &v1alpha2.PullImageResponse{}, nil
}

func (s *Server) CreateMachine(_ context.Context, r *v1alpha2.CreateMachineRequest) (*v1alpha2.CreateMachineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("CreateMachine", r.GetMachineId())
	if s.MaxVCPUs > 0 && r.GetResources().GetCpuCount() > s.MaxVCPUs {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "cpu count is above the driver's max_vcpus")
	}
	if k := r.GetKernel(); k != "" && len(s.Kernels) > 0 && !slices.Contains(s.Kernels, k) {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "the node offers no kernel "+k)
	}
	if err := s.checkProcess(r); err != nil {
		return nil, err
	}
	if _, ok := s.machines[r.GetMachineId()]; ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_CONFLICT, "machine exists")
	}
	for _, a := range r.GetDisks() {
		d, ok := s.disks[a.GetDiskId()]
		switch {
		case !ok:
			return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "no disk "+a.GetDiskId())
		case d.HolderMachineId != "":
			return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "disk "+a.GetDiskId()+" is held by "+d.HolderMachineId)
		}
	}
	for _, a := range r.GetDisks() {
		s.disks[a.GetDiskId()].HolderMachineId = r.GetMachineId()
	}
	m := &v1alpha2.Machine{
		Id:        r.GetMachineId(),
		State:     v1alpha2.MachineState_MACHINE_STATE_CREATED,
		Resources: r.GetResources(),
		Kernel:    r.GetKernel(),
		Addresses: []string{"10.0.0.2"},

		Disks:         r.GetDisks(),
		NetworkPolicy: r.GetNetworkPolicy(),
	}
	s.machines[m.Id] = m
	s.requests[m.Id] = proto.Clone(r).(*v1alpha2.CreateMachineRequest)
	return &v1alpha2.CreateMachineResponse{Machine: proto.Clone(m).(*v1alpha2.Machine)}, nil
}

func (s *Server) StartMachine(_ context.Context, r *v1alpha2.StartMachineRequest) (*v1alpha2.StartMachineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("StartMachine", r.GetMachineId())
	m, ok := s.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	if !s.armed() {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "fencing deadline is not armed")
	}
	m.State = v1alpha2.MachineState_MACHINE_STATE_RUNNING
	return &v1alpha2.StartMachineResponse{Machine: proto.Clone(m).(*v1alpha2.Machine)}, nil
}

func (s *Server) StopMachine(_ context.Context, r *v1alpha2.StopMachineRequest) (*v1alpha2.StopMachineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("StopMachine", r.GetMachineId())
	m, ok := s.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	if m.State != v1alpha2.MachineState_MACHINE_STATE_CREATED {
		m.State = v1alpha2.MachineState_MACHINE_STATE_STOPPED
	}
	return &v1alpha2.StopMachineResponse{Machine: proto.Clone(m).(*v1alpha2.Machine)}, nil
}

func (s *Server) DeleteMachine(_ context.Context, r *v1alpha2.DeleteMachineRequest) (*v1alpha2.DeleteMachineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("DeleteMachine", r.GetMachineId())
	if s.failDeletes {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "delete refused")
	}
	m, ok := s.machines[r.GetMachineId()]
	if !ok {
		return &v1alpha2.DeleteMachineResponse{}, nil
	}
	if m.State == v1alpha2.MachineState_MACHINE_STATE_RUNNING || m.State == v1alpha2.MachineState_MACHINE_STATE_PAUSED {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "machine is running")
	}
	for _, d := range s.disks {
		if d.HolderMachineId == r.GetMachineId() {
			d.HolderMachineId = ""
		}
	}
	delete(s.machines, r.GetMachineId())
	return &v1alpha2.DeleteMachineResponse{}, nil
}

// CreateDisk makes a disk no Machine holds, and refuses an identifier mvmd
// already holds.
func (s *Server) CreateDisk(_ context.Context, r *v1alpha2.CreateDiskRequest) (*v1alpha2.CreateDiskResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("CreateDisk", r.GetDiskId())
	if s.diskFailure != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED {
		return nil, v1alpha2.NewError(s.diskFailure, "the node cannot make the disk")
	}
	if _, ok := s.disks[r.GetDiskId()]; ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_CONFLICT, "disk exists")
	}
	d := &v1alpha2.Disk{Id: r.GetDiskId(), SizeBytes: r.GetSizeBytes()}
	s.disks[d.Id] = d
	return &v1alpha2.CreateDiskResponse{Disk: proto.Clone(d).(*v1alpha2.Disk)}, nil
}

// ListDisks answers the disks ordered by identifier.
func (s *Server) ListDisks(context.Context, *v1alpha2.ListDisksRequest) (*v1alpha2.ListDisksResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &v1alpha2.ListDisksResponse{}
	for _, d := range s.disks {
		out.Disks = append(out.Disks, proto.Clone(d).(*v1alpha2.Disk))
	}
	slices.SortFunc(out.Disks, func(a, b *v1alpha2.Disk) int { return strings.Compare(a.Id, b.Id) })
	return out, nil
}

// DeleteDisk deletes a disk no Machine holds.
func (s *Server) DeleteDisk(_ context.Context, r *v1alpha2.DeleteDiskRequest) (*v1alpha2.DeleteDiskResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("DeleteDisk", r.GetDiskId())
	d, ok := s.disks[r.GetDiskId()]
	switch {
	case !ok:
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such disk")
	case d.HolderMachineId != "":
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "disk is held by "+d.HolderMachineId)
	}
	delete(s.disks, r.GetDiskId())
	return &v1alpha2.DeleteDiskResponse{}, nil
}

func (s *Server) InspectMachine(_ context.Context, r *v1alpha2.InspectMachineRequest) (*v1alpha2.InspectMachineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	return &v1alpha2.InspectMachineResponse{Machine: proto.Clone(m).(*v1alpha2.Machine)}, nil
}

// ReadExecOutput answers a read of the exec "entrypoint" with the record set
// for the Machine, or as mvmd does for a Machine none of whose console lines
// speaks of the entrypoint, with a record that says it was cut short.
func (s *Server) ReadExecOutput(r *v1alpha2.ReadExecOutputRequest, stream grpc.ServerStreamingServer[v1alpha2.ReadExecOutputResponse]) error {
	s.mu.Lock()
	_, held := s.machines[r.GetMachineId()]
	rec, set := s.records[r.GetMachineId()]
	silent := s.silent[r.GetMachineId()]
	s.mu.Unlock()
	if !held || r.GetExecId() != "entrypoint" {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such exec")
	}
	if silent {
		return nil
	}
	if !set {
		rec = &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: v1alpha2.ExecStatus_EXEC_STATUS_CUT_SHORT}
	}
	return stream.Send(&v1alpha2.ReadExecOutputResponse{Part: &v1alpha2.ReadExecOutputResponse_Record{Record: rec}})
}

func (s *Server) ListMachines(context.Context, *v1alpha2.ListMachinesRequest) (*v1alpha2.ListMachinesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &v1alpha2.ListMachinesResponse{}
	for _, m := range s.machines {
		out.Machines = append(out.Machines, proto.Clone(m).(*v1alpha2.Machine))
	}
	return out, nil
}

func (s *Server) WatchEvents(_ *v1alpha2.WatchEventsRequest, stream v1alpha2.RuntimeService_WatchEventsServer) error {
	for {
		select {
		case ev := <-s.events:
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// RenewDeadline applies a renewal whose number is above the last applied, as
// mvmd does: the expiry is the length from receipt.
func (s *Server) RenewDeadline(_ context.Context, r *v1alpha2.RenewDeadlineRequest) (*v1alpha2.RenewDeadlineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.GetRenewalNumber() > s.last() {
		length := r.GetLength().AsDuration()
		s.renewals = append(s.renewals, Renewal{Number: r.GetRenewalNumber(), Length: length, Expiry: s.clock.Now().Add(length)})
	}
	return &v1alpha2.RenewDeadlineResponse{Status: s.deadlineStatus()}, nil
}

func (s *Server) InspectDeadline(context.Context, *v1alpha2.InspectDeadlineRequest) (*v1alpha2.InspectDeadlineResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &v1alpha2.InspectDeadlineResponse{Status: s.deadlineStatus()}, nil
}

func (s *Server) last() uint64 {
	if len(s.renewals) == 0 {
		return 0
	}
	return s.renewals[len(s.renewals)-1].Number
}

func (s *Server) armed() bool {
	return len(s.renewals) > 0 && s.clock.Now().Before(s.renewals[len(s.renewals)-1].Expiry)
}

func (s *Server) deadlineStatus() *v1alpha2.DeadlineStatus {
	st := &v1alpha2.DeadlineStatus{Phase: v1alpha2.DeadlinePhase_DEADLINE_PHASE_NOT_ARMED, LastRenewalNumber: s.last()}
	if len(s.renewals) > 0 {
		latest := s.renewals[len(s.renewals)-1]
		st.Length = durationpb.New(latest.Length)
		st.Phase = v1alpha2.DeadlinePhase_DEADLINE_PHASE_ARMED
		if remaining := latest.Expiry.Sub(s.clock.Now()); remaining > 0 {
			st.Remaining = durationpb.New(remaining)
		} else {
			st.Phase = v1alpha2.DeadlinePhase_DEADLINE_PHASE_EXPIRED
		}
	}
	return st
}

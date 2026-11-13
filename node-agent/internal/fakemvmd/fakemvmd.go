// Package fakemvmd is an in-memory mvmd serving the runtime API v1alpha2 on a
// Unix socket, for the node agent's tests.
package fakemvmd

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/garamsh/gaon/lib/plugin/plugintest"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Renewal is one RenewDeadline call mvmd applied.
type Renewal struct {
	Number uint64
	Length time.Duration
	// Expiry is the clock's reading at receipt plus the length.
	Expiry time.Time
}

// Server is the fake. Its methods are safe for concurrent use.
type Server struct {
	v1alpha2.UnimplementedRuntimeServiceServer

	// Socket is the path the server listens on.
	Socket string
	// MaxVCPUs, when above zero, is the most vCPUs a Create accepts.
	MaxVCPUs uint32
	// Kernels, when not empty, are the kernel names the node offers: a Create
	// naming another is rejected. An empty name is always accepted.
	Kernels []string

	clock       clock.Clock
	mu          sync.Mutex
	machines    map[string]*v1alpha2.Machine
	requests    map[string]*v1alpha2.CreateMachineRequest
	legacy      map[string]bool
	images      []string
	calls       []string
	renewals    []Renewal
	events      chan *v1alpha2.WatchEventsResponse
	sequence    uint64
	capacity    *v1alpha2.NodeCapacity
	prefixes    []string
	failDeletes bool
	disks       map[string]*v1alpha2.Disk
	diskFailure v1alpha2.OutcomeKind
	records     map[string]*v1alpha2.ExecRecord
	silent      map[string]bool
	guest       map[string]map[string][]byte
	failWrites  bool
}

// New starts a fake mvmd on a socket in a plugintest.SocketDir, reading time from clk,
// and stops it when the test ends.
func New(t testing.TB, clk clock.Clock) *Server {
	t.Helper()
	// A socket path is limited to about 100 bytes, so it sits in a short
	// directory of its own.
	s := &Server{
		Socket:   filepath.Join(plugintest.SocketDir(t), "mvmd.sock"),
		clock:    clk,
		machines: map[string]*v1alpha2.Machine{},
		requests: map[string]*v1alpha2.CreateMachineRequest{},
		legacy:   map[string]bool{},
		disks:    map[string]*v1alpha2.Disk{},
		records:  map[string]*v1alpha2.ExecRecord{},
		silent:   map[string]bool{},
		guest:    map[string]map[string][]byte{},
		events:   make(chan *v1alpha2.WatchEventsResponse, 256),
		capacity: &v1alpha2.NodeCapacity{Cpus: 8, MemoryBytes: 16 << 30, StateTotalBytes: 100 << 30, StateAvailableBytes: 90 << 30},
	}
	lis, err := net.Listen("unix", s.Socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", s.Socket, err)
	}
	g := grpc.NewServer()
	v1alpha2.RegisterRuntimeServiceServer(g, s)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return s
}

// SetMachinePrefixes sets the prefixes the runtime info reports as the ones
// Machine addresses are assigned from.
func (s *Server) SetMachinePrefixes(prefixes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefixes = prefixes
}

// Calls returns the lifecycle, image and disk-making calls received, in order, each as
// "<Call> <id>".
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// CreateRequest returns a copy of the request that created the Machine id, as
// the Machine's record holds it now: the env and files of the latest start's
// run update replace the ones it was created with. It reports whether mvmd
// holds the Machine.
func (s *Server) CreateRequest(id string) (*v1alpha2.CreateMachineRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(r).(*v1alpha2.CreateMachineRequest), true
}

// SetLegacyImage marks the image digest as pulled before mvmd kept an
// image's entrypoint and command apart: a Create naming it with args and no
// command is refused as PRECONDITION_FAILED.
func (s *Server) SetLegacyImage(digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacy[digest] = true
}

// Renewals returns the deadline renewals mvmd applied.
func (s *Server) Renewals() []Renewal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Renewal(nil), s.renewals...)
}

// Machine returns a copy of the Machine id, and whether mvmd holds it.
func (s *Server) Machine(id string) (*v1alpha2.Machine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(m).(*v1alpha2.Machine), true
}

// IDs returns the identifiers of the Machines mvmd holds.
func (s *Server) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.machines))
	for id := range s.machines {
		ids = append(ids, id)
	}
	return ids
}

// Images returns the image digests pulled.
func (s *Server) Images() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.images...)
}

// SetState moves the Machine id to state with reason, as mvmd does when a
// VMM ends on its own, and emits the state change.
func (s *Server) SetState(id string, state v1alpha2.MachineState, reason string) {
	s.mu.Lock()
	m := s.machines[id]
	m.State, m.LastFailureReason = state, reason
	s.mu.Unlock()
	s.emit(id, &v1alpha2.WatchEventsResponse{Event: &v1alpha2.WatchEventsResponse_StateChanged{StateChanged: &v1alpha2.StateChanged{State: state}}})
}

// SetEntrypointRecord sets the record a read of the exec "entrypoint" of the
// Machine id answers with, as the console log of a stopped Machine gives it.
func (s *Server) SetEntrypointRecord(id string, r *v1alpha2.ExecRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = r
}

// SilenceEntrypoint makes a read of the Machine id's entrypoint end its
// stream with no record while on is true.
func (s *Server) SilenceEntrypoint(id string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.silent[id] = on
}

// FailDeletes makes every DeleteMachine fail with a refusal while on
// is true, so mvmd keeps what it holds.
func (s *Server) FailDeletes(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDeletes = on
}

// FailDiskCreates makes every CreateDisk fail with a refusal while on is
// true, as mvmd does for a disk it cannot make.
func (s *Server) FailDiskCreates(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diskFailure = v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED
	if on {
		s.diskFailure = v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED
	}
}

// FailDiskCreatesWith makes every CreateDisk fail with an error of kind, until
// it is called with OUTCOME_KIND_UNSPECIFIED.
func (s *Server) FailDiskCreatesWith(kind v1alpha2.OutcomeKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diskFailure = kind
}

// AddDisk makes a disk no Machine holds, as a storage plugin does through
// CreateDisk, without recording a call.
func (s *Server) AddDisk(id string, sizeBytes uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disks[id] = &v1alpha2.Disk{Id: id, SizeBytes: sizeBytes}
}

// Disk returns a copy of the disk id, and whether mvmd holds it.
func (s *Server) Disk(id string) (*v1alpha2.Disk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.disks[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(d).(*v1alpha2.Disk), true
}

// Arm applies a deadline renewal as a client of mvmd would.
func (s *Server) Arm(length time.Duration, number uint64) {
	_, _ = s.RenewDeadline(context.Background(), &v1alpha2.RenewDeadlineRequest{Length: durationpb.New(length), RenewalNumber: number})
}

func (s *Server) emit(id string, ev *v1alpha2.WatchEventsResponse) {
	s.mu.Lock()
	s.sequence++
	ev.Sequence, ev.MachineId = s.sequence, id
	s.mu.Unlock()
	select {
	case s.events <- ev:
	default:
	}
}

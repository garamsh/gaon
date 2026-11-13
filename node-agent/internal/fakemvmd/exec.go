package fakemvmd

import (
	"context"

	"google.golang.org/grpc"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// execState is the output and record of one exec of a Machine. Its output is
// read from offset zero up, and a read waits for more while record is nil.
type execState struct {
	chunks   []*v1alpha2.OutputChunk
	next     uint64
	record   *v1alpha2.ExecRecord
	earliest uint64
	wake     chan struct{}
}

func newExecState() *execState { return &execState{wake: make(chan struct{})} }

// changed wakes the reads waiting on e. The caller holds the server's lock.
func (e *execState) changed() {
	close(e.wake)
	e.wake = make(chan struct{})
}

// ExecPlan is what an Exec of the Machine does: the record it answers with and
// the output it makes. A Status of STARTED leaves the exec running until
// FinishExec.
type ExecPlan struct {
	Status   v1alpha2.ExecStatus
	ExitCode int32
	Reason   string
	Stdout   string
	Stderr   string
}

func execKey(id, execID string) string { return id + "\x00" + execID }

// state returns the exec's state, made when it is missing. The caller holds s.mu.
func (s *Server) state(id, execID string) *execState {
	k := execKey(id, execID)
	e, ok := s.execs[k]
	if !ok {
		e = newExecState()
		s.execs[k] = e
	}
	return e
}

// AppendOutput adds data to the output of the exec of the Machine id on
// stream, as the guest does while the command runs.
func (s *Server) AppendOutput(id, execID string, stream v1alpha2.OutputStream, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.state(id, execID)
	e.chunks = append(e.chunks, &v1alpha2.OutputChunk{Stream: stream, Offset: e.next, Data: []byte(data)})
	e.next += uint64(len(data))
	e.changed()
}

// FinishExec ends the exec of the Machine id with record, which ends a read of
// its output.
func (s *Server) FinishExec(id, execID string, record *v1alpha2.ExecRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.state(id, execID)
	record.ExecId = execID
	e.record = record
	e.changed()
}

// DropOutputBefore forgets the output of the exec before offset, as mvmd does
// with a long output: a read from before it fails with OutputGone.
func (s *Server) DropOutputBefore(id, execID string, offset uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state(id, execID).earliest = offset
}

// PlanExec sets what every Exec does from now on.
func (s *Server) PlanExec(p ExecPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = p
}

// FailExecsWith makes every Exec fail with an error of kind until it is
// called with the zero kind.
func (s *Server) FailExecsWith(kind v1alpha2.OutcomeKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execFailure = kind
}

// FailReadsWith makes every ReadExecOutput fail with an error of kind until it
// is called with the zero kind.
func (s *Server) FailReadsWith(kind v1alpha2.OutcomeKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readFailure = kind
}

// ExecRequests returns every Exec request received, in order.
func (s *Server) ExecRequests() []*v1alpha2.ExecRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*v1alpha2.ExecRequest(nil), s.execCalls...)
}

// ReadOpens returns the after_offset of every ReadExecOutput request for the
// exec of the Machine id, in order.
func (s *Server) ReadOpens(id, execID string) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.opens[execKey(id, execID)]...)
}

func (s *Server) Exec(_ context.Context, r *v1alpha2.ExecRequest) (*v1alpha2.ExecResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execCalls = append(s.execCalls, r)
	if s.execFailure != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED {
		return nil, v1alpha2.NewError(s.execFailure, "exec refused")
	}
	if _, ok := s.machines[r.GetMachineId()]; !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	e := s.state(r.GetMachineId(), r.GetExecId())
	rec := &v1alpha2.ExecRecord{ExecId: r.GetExecId(), Status: s.plan.Status, ExitCode: s.plan.ExitCode, Reason: s.plan.Reason}
	if s.plan.Stdout != "" {
		e.chunks = append(e.chunks, &v1alpha2.OutputChunk{Stream: v1alpha2.OutputStream_OUTPUT_STREAM_STDOUT, Offset: e.next, Data: []byte(s.plan.Stdout)})
		e.next += uint64(len(s.plan.Stdout))
	}
	if s.plan.Stderr != "" {
		e.chunks = append(e.chunks, &v1alpha2.OutputChunk{Stream: v1alpha2.OutputStream_OUTPUT_STREAM_STDERR, Offset: e.next, Data: []byte(s.plan.Stderr)})
		e.next += uint64(len(s.plan.Stderr))
	}
	if s.plan.Status != v1alpha2.ExecStatus_EXEC_STATUS_STARTED {
		e.record = rec
	}
	e.changed()
	return &v1alpha2.ExecResponse{Record: rec}, nil
}

// serveExec answers a read of an exec that has a state: its chunks from the
// offset, then, once it has ended, its record, waiting for more while it runs.
func (s *Server) serveExec(ctx context.Context, e *execState, after uint64, stream grpc.ServerStreamingServer[v1alpha2.ReadExecOutputResponse]) error {
	sent := after
	for {
		s.mu.Lock()
		if sent < e.earliest {
			earliest := e.earliest
			s.mu.Unlock()
			return v1alpha2.NewOutputGone("output no longer held", earliest)
		}
		var pending []*v1alpha2.OutputChunk
		for _, c := range e.chunks {
			if c.GetOffset()+uint64(len(c.GetData())) > sent {
				pending = append(pending, c)
			}
		}
		record, wake := e.record, e.wake
		s.mu.Unlock()
		for _, c := range pending {
			if err := stream.Send(&v1alpha2.ReadExecOutputResponse{Part: &v1alpha2.ReadExecOutputResponse_Chunk{Chunk: c}}); err != nil {
				return err
			}
			sent = c.GetOffset() + uint64(len(c.GetData()))
		}
		if record != nil {
			return stream.Send(&v1alpha2.ReadExecOutputResponse{Part: &v1alpha2.ReadExecOutputResponse_Record{Record: record}})
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

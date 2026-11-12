package fakemvmd

import (
	"errors"
	"io"
	"path"

	"google.golang.org/grpc"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// resend puts the files of the create request of id in the guest, as mvmd
// does at every boot: a file written since is replaced by its create-time
// content. The caller holds s.mu.
func (s *Server) resend(id string) {
	files := map[string][]byte{}
	for _, f := range s.requests[id].GetFiles() {
		files[f.GetPath()] = append([]byte(nil), f.GetContent()...)
	}
	s.guest[id] = files
}

// File returns a copy of the content of the file at p in the guest of the
// Machine id, and whether the guest holds it.
func (s *Server) File(id, p string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.guest[id][p]
	return append([]byte(nil), c...), ok
}

// FailWriteFiles makes every WriteFile end with an error after its chunks, so
// that the guest keeps the old content, as when a stream breaks.
func (s *Server) FailWriteFiles(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWrites = on
}

// WriteFile commits the streamed content whole at the end of a clean stream
// and leaves the old content when the stream breaks.
func (s *Server) WriteFile(stream grpc.ClientStreamingServer[v1alpha2.WriteFileRequest, v1alpha2.WriteFileResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "the first message is not a start")
	}
	var content []byte
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		content = append(content, msg.GetChunk()...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("WriteFile", start.GetMachineId())
	m, ok := s.machines[start.GetMachineId()]
	switch {
	case !ok:
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	case m.State != v1alpha2.MachineState_MACHINE_STATE_RUNNING:
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "the machine is not running")
	case !path.IsAbs(start.GetPath()) || path.Clean(start.GetPath()) != start.GetPath():
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "the path is not absolute and clean")
	case s.failWrites:
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED, "the stream broke")
	}
	s.guest[start.GetMachineId()][start.GetPath()] = content
	return stream.SendAndClose(&v1alpha2.WriteFileResponse{})
}

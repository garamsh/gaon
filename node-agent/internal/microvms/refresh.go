package microvms

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// chunkSize is the most bytes of a file the agent sends in one message.
const chunkSize = 64 << 10

// deliveredDigests is the digest of each file of req whose value comes from a
// Secret or a ConfigMap, by path.
func deliveredDigests(m *api.MicroVM, req *v1alpha2.CreateMachineRequest) map[string][sha256.Size]byte {
	digests := map[string][sha256.Size]byte{}
	for i, f := range m.Spec.Files {
		if f.ValueFrom != nil {
			digests[f.Path] = sha256.Sum256(req.GetFiles()[i].GetContent())
		}
	}
	return digests
}

// refresh brings the files of a running MicroVM up to date: a file whose value
// comes from a Secret or a ConfigMap is replaced when the object's value
// differs from what the guest holds, and the ServiceAccount token is replaced
// once 80% of its lifetime has passed. A failure is logged with the names
// involved and retried on the next round; it reports whether the token has
// expired in the guest. Env is not touched: it is fixed when the MicroVM
// starts.
func (s *service) refresh(ctx context.Context, m *api.MicroVM, id string) (tokenExpired bool) {
	st := s.runOf(id)
	s.refreshFiles(ctx, m, id, st)
	if m.Spec.ServiceAccountName == "" {
		return false
	}
	if now := s.clock.Now(); st.token.renewAt.IsZero() || !now.Before(st.token.renewAt) {
		if err := s.renewToken(ctx, m, id, st); err != nil {
			s.log.Warn("cannot renew the token", "microvm", m.Metadata.Name, "serviceaccount", m.Spec.ServiceAccountName, "error", err)
		}
	}
	return !st.token.expiresAt.IsZero() && !s.clock.Now().Before(st.token.expiresAt)
}

// refreshFiles reads the Secrets and ConfigMaps the files name and writes the
// files whose value changed since the last write. The Node authorizer allows a
// get of each and no watch, so the objects are read on every round. The
// writes are skipped when the files, once resolved, are over mvmd's bound.
func (s *service) refreshFiles(ctx context.Context, m *api.MicroVM, id string, st *run) {
	refs := newReferences(s.api)
	type change struct {
		path    string
		content []byte
	}
	var changes []change
	total := 0
	for _, f := range m.Spec.Files {
		content := []byte(f.Content)
		if f.ValueFrom != nil {
			var err error
			if content, err = refs.resolve(ctx, f.ValueFrom); err != nil {
				s.log.Warn("cannot read the value of a file", "microvm", m.Metadata.Name, "path", f.Path, "error", err)
				continue
			}
			if digest, ok := st.delivered[f.Path]; !ok || digest != sha256.Sum256(content) {
				changes = append(changes, change{f.Path, content})
			}
		}
		total += len(content)
	}
	if total > api.MaxFilesBytes {
		s.log.Warn("the files are over the bound once resolved, and are not replaced", "microvm", m.Metadata.Name, "bound", api.MaxFilesBytes)
		return
	}
	for _, c := range changes {
		if err := s.writeFile(ctx, id, c.path, c.content); err != nil {
			s.log.Warn("cannot replace a file", "microvm", m.Metadata.Name, "path", c.path, "error", err)
			continue
		}
		if st.delivered == nil {
			st.delivered = map[string][sha256.Size]byte{}
		}
		st.delivered[c.path] = sha256.Sum256(c.content)
		s.log.Info("replaced file", "microvm", m.Metadata.Name, "path", c.path)
	}
}

// renewToken requests a new token and replaces the token file with it.
func (s *service) renewToken(ctx context.Context, m *api.MicroVM, id string, st *run) error {
	token, state, err := s.requestToken(ctx, m)
	if err != nil {
		return err
	}
	if err := s.writeFile(ctx, id, TokenPath, token); err != nil {
		return err
	}
	state.createdExpiresAt = st.token.createdExpiresAt
	st.token = state
	return nil
}

// writeFile replaces the file at path in the guest of id with content. mvmd
// commits it whole at the end of the stream, by a rename at the same path, and
// keeps the old content when the stream breaks.
func (s *service) writeFile(ctx context.Context, id, path string, content []byte) error {
	stream, err := s.runtime.WriteFile(ctx)
	if err != nil {
		return fmt.Errorf("write %s in %s: %w", path, id, err)
	}
	sent := stream.Send(&v1alpha2.WriteFileRequest{Part: &v1alpha2.WriteFileRequest_Start{Start: &v1alpha2.WriteFileStart{MachineId: id, Path: path}}})
	for rest := content; sent == nil && len(rest) > 0; {
		n := min(len(rest), chunkSize)
		sent = stream.Send(&v1alpha2.WriteFileRequest{Part: &v1alpha2.WriteFileRequest_Chunk{Chunk: rest[:n]}})
		rest = rest[n:]
	}
	// A failed send ends the stream; the error of the call is the status.
	if _, err := stream.CloseAndRecv(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("write %s in %s: %w", path, id, err)
	}
	if sent != nil && !errors.Is(sent, io.EOF) {
		return fmt.Errorf("write %s in %s: %w", path, id, sent)
	}
	return nil
}

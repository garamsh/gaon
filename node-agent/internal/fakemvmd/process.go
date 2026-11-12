package fakemvmd

import (
	"path"
	"strings"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// The bounds mvmd holds a Create's files to.
const (
	maxFiles      = 64
	maxFilesBytes = 1 << 20
	maxFileMode   = 0o7777
)

// checkProcess refuses a Create the way mvmd refuses one whose command, args,
// env, user or files it cannot take. The caller holds s.mu. A message names
// the field and the name or path and never a value or a content.
func (s *Server) checkProcess(r *v1alpha2.CreateMachineRequest) error {
	rejected := func(msg string) error {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, msg)
	}
	if len(r.GetArgs()) > 0 && len(r.GetCommand()) == 0 && s.legacy[r.GetImageDigest()] {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED,
			"image "+r.GetImageDigest()+" holds one joined argument vector, so args alone has no command to replace: pull it again")
	}
	names := map[string]bool{}
	for _, e := range r.GetEnv() {
		if e.GetName() == "" || strings.Contains(e.GetName(), "=") || names[e.GetName()] {
			return rejected("env name " + e.GetName() + " is empty, contains '=' or is given twice")
		}
		names[e.GetName()] = true
	}
	if len(r.GetFiles()) > maxFiles {
		return rejected("files holds more than 64 files")
	}
	paths := map[string]bool{}
	total := 0
	for _, f := range r.GetFiles() {
		p := f.GetPath()
		switch {
		case !path.IsAbs(p) || path.Clean(p) != p || p == "/":
			return rejected("file path " + p + " is not absolute and clean")
		case paths[p]:
			return rejected("file path " + p + " is given twice")
		case f.GetMode() > maxFileMode:
			return rejected("file " + p + " has a mode above 07777")
		}
		paths[p] = true
		total += len(f.GetContent())
	}
	if total > maxFilesBytes {
		return rejected("files hold more than 1 MiB of content")
	}
	return nil
}

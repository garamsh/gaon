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
// env, user, working directory or files it cannot take. The caller holds s.mu.
// A message names the field and the name or path and never a value or a
// content.
func (s *Server) checkProcess(r *v1alpha2.CreateMachineRequest) error {
	if len(r.GetArgs()) > 0 && len(r.GetCommand()) == 0 && s.legacy[r.GetImageDigest()] {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED,
			"image "+r.GetImageDigest()+" holds one joined argument vector, so args alone has no command to replace: pull it again")
	}
	if d := r.GetWorkingDir(); d != "" && (!path.IsAbs(d) || path.Clean(d) != d) {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "working_dir "+d+" is not absolute and clean")
	}
	return checkEnvFiles(r.GetEnv(), r.GetFiles())
}

// checkEnvFiles refuses env and files as mvmd does at a Create and at a Start
// with a run update.
func checkEnvFiles(env []*v1alpha2.EnvVar, files []*v1alpha2.File) error {
	rejected := func(msg string) error {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, msg)
	}
	names := map[string]bool{}
	for _, e := range env {
		if e.GetName() == "" || strings.Contains(e.GetName(), "=") || names[e.GetName()] {
			return rejected("env name " + e.GetName() + " is empty, contains '=' or is given twice")
		}
		names[e.GetName()] = true
	}
	if len(files) > maxFiles {
		return rejected("files holds more than 64 files")
	}
	paths := map[string]bool{}
	total := 0
	for _, f := range files {
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

package api

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The bounds on a MicroVM's files, those of mvmd's create request. The Node
// Agent resolves the references of files and refuses a MicroVM whose resolved
// content exceeds MaxFilesBytes; validation can bound the inline content only.
const (
	MaxFiles      = 64
	MaxFilesBytes = 1 << 20
)

// DefaultFileMode is the mode of a file that gives none.
const DefaultFileMode int32 = 0o644

var (
	envName = regexp.MustCompile(`^[-._a-zA-Z][-._a-zA-Z0-9]*$`)
	userID  = regexp.MustCompile(`^[^:\s]+(:[^:\s]+)?$`)
)

// KeyReference names a key of the Secret or the ConfigMap Name.
type KeyReference struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// ValueSource says where a value comes from: exactly one of a Secret's key or
// a ConfigMap's key. It is resolved when the MicroVM starts and is not
// updated while the MicroVM runs.
type ValueSource struct {
	SecretKeyRef    *KeyReference `json:"secretKeyRef,omitempty"`
	ConfigMapKeyRef *KeyReference `json:"configMapKeyRef,omitempty"`
}

// EnvVar is an environment variable of the MicroVM's process, given as Value
// or as ValueFrom.
type EnvVar struct {
	Name      string       `json:"name"`
	Value     string       `json:"value,omitempty"`
	ValueFrom *ValueSource `json:"valueFrom,omitempty"`
}

// FileOwner is the owner of a file in the guest.
type FileOwner struct {
	UID int64 `json:"uid"`
	GID int64 `json:"gid"`
}

// MicroVMFile is a file written into the guest before the process starts,
// given as Content or as ValueFrom. A nil Mode is DefaultFileMode. A nil
// Owner is the MicroVM's user, and root when it gives none.
type MicroVMFile struct {
	Path      string       `json:"path"`
	Mode      *int32       `json:"mode,omitempty"`
	Owner     *FileOwner   `json:"owner,omitempty"`
	Content   string       `json:"content,omitempty"`
	ValueFrom *ValueSource `json:"valueFrom,omitempty"`
}

func (v *ValueSource) validate(field string) error {
	if v == nil {
		return nil
	}
	var ref *KeyReference
	switch {
	case v.SecretKeyRef != nil && v.ConfigMapKeyRef != nil:
		return invalid("%s names both secretKeyRef and configMapKeyRef, want one", field)
	case v.SecretKeyRef != nil:
		ref = v.SecretKeyRef
	case v.ConfigMapKeyRef != nil:
		ref = v.ConfigMapKeyRef
	default:
		return invalid("%s names neither secretKeyRef nor configMapKeyRef, want one", field)
	}
	if err := validateObjectName(field+".name", ref.Name); err != nil {
		return err
	}
	return validateDataKey(field+".key", ref.Key)
}

func validateProcess(s *MicroVMSpec) error {
	if len(s.Command) > 0 && s.Command[0] == "" {
		return invalid("spec.command[0] is empty, want the program to run")
	}
	seen := map[string]bool{}
	for i, e := range s.Env {
		if !envName.MatchString(e.Name) {
			return invalid("spec.env[%d].name is %q, want letters, digits, '-', '_' or '.', not starting with a digit", i, e.Name)
		}
		if seen[e.Name] {
			return invalid("spec.env[%d].name %q is used twice", i, e.Name)
		}
		seen[e.Name] = true
		if e.Value != "" && e.ValueFrom != nil {
			return invalid("spec.env[%d] gives both value and valueFrom, want one", i)
		}
		if err := e.ValueFrom.validate("spec.env[" + strconv.Itoa(i) + "].valueFrom"); err != nil {
			return err
		}
	}
	if s.User != "" && !userID.MatchString(s.User) {
		return invalid("spec.user is %q, want a user or user:group, each non-empty", s.User)
	}
	if s.WorkingDir != "" && !path.IsAbs(s.WorkingDir) {
		return invalid("spec.workingDir is %q, want an absolute path", s.WorkingDir)
	}
	return validateFiles(s.Files)
}

func validateFiles(files []MicroVMFile) error {
	if len(files) > MaxFiles {
		return invalid("spec.files holds %d files, want at most %d", len(files), MaxFiles)
	}
	seen := map[string]bool{}
	inline := 0
	for i, f := range files {
		field := "spec.files[" + strconv.Itoa(i) + "]"
		if !path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || f.Path == "/" || strings.ContainsRune(f.Path, 0) {
			return invalid("%s.path is %q, want a clean absolute path below /", field, f.Path)
		}
		if seen[f.Path] {
			return invalid("%s.path %q is used twice", field, f.Path)
		}
		seen[f.Path] = true
		if f.Mode != nil && (*f.Mode < 0 || *f.Mode > 0o777) {
			return invalid("%s.mode is %#o, want 0 to 0777", field, *f.Mode)
		}
		if f.Owner != nil && (f.Owner.UID < 0 || f.Owner.GID < 0) {
			return invalid("%s.owner is %d:%d, want a uid and a gid of 0 or more", field, f.Owner.UID, f.Owner.GID)
		}
		if f.Content != "" && f.ValueFrom != nil {
			return invalid("%s gives both content and valueFrom, want one", field)
		}
		if err := f.ValueFrom.validate(field + ".valueFrom"); err != nil {
			return err
		}
		inline += len(f.Content)
	}
	if inline > MaxFilesBytes {
		return invalid("spec.files holds %d bytes of content, want at most %d", inline, MaxFilesBytes)
	}
	return nil
}

// ReferencesSecret reports whether m's environment or files take a value from
// the Secret named name. The Node authorizer lets a Node read a Secret only
// when a MicroVM bound to it does.
func (m *MicroVM) ReferencesSecret(name string) bool {
	return m.references(func(v *ValueSource) bool {
		return v.SecretKeyRef != nil && v.SecretKeyRef.Name == name
	})
}

// ReferencesConfigMap reports whether m's environment or files take a value
// from the ConfigMap named name.
func (m *MicroVM) ReferencesConfigMap(name string) bool {
	return m.references(func(v *ValueSource) bool {
		return v.ConfigMapKeyRef != nil && v.ConfigMapKeyRef.Name == name
	})
}

func (m *MicroVM) references(match func(*ValueSource) bool) bool {
	for _, e := range m.Spec.Env {
		if e.ValueFrom != nil && match(e.ValueFrom) {
			return true
		}
	}
	for _, f := range m.Spec.Files {
		if f.ValueFrom != nil && match(f.ValueFrom) {
			return true
		}
	}
	return false
}

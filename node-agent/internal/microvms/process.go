package microvms

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// specFailure is a failure of a MicroVM that the spec or the objects it
// references settle, with the reason it is reported under. A message names
// an object and a key and never a value.
type specFailure struct {
	reason string
	err    error
}

func (e specFailure) Error() string { return e.err.Error() }
func (e specFailure) Unwrap() error { return e.err }

// withProcess puts the MicroVM's command, args, env, user and files in req.
// It reads each Secret and ConfigMap the spec references once, with a get
// under the Node's own identity. A missing object or key, a file that is over
// the bound once resolved, and a file whose owner cannot be decided are
// returned as a specFailure; any other error is retried.
func (s *service) withProcess(ctx context.Context, m *api.MicroVM, req *v1alpha2.CreateMachineRequest) error {
	spec := &m.Spec
	req.Command, req.Args, req.User = slices.Clone(spec.Command), slices.Clone(spec.Args), spec.User
	refs := references{api: s.api, secrets: map[string]*api.Secret{}, configMaps: map[string]*api.ConfigMap{}}
	for _, e := range spec.Env {
		value := []byte(e.Value)
		if e.ValueFrom != nil {
			var err error
			if value, err = refs.resolve(ctx, e.ValueFrom); err != nil {
				return fmt.Errorf("env %s: %w", e.Name, err)
			}
		}
		req.Env = append(req.Env, &v1alpha2.EnvVar{Name: e.Name, Value: string(value)})
	}
	if len(spec.Files) > api.MaxFiles {
		return specFailure{ReasonFilesOverBound, fmt.Errorf("spec.files holds %d files, want at most %d", len(spec.Files), api.MaxFiles)}
	}
	total := 0
	for _, f := range spec.Files {
		content := []byte(f.Content)
		if f.ValueFrom != nil {
			var err error
			if content, err = refs.resolve(ctx, f.ValueFrom); err != nil {
				return fmt.Errorf("file %s: %w", f.Path, err)
			}
		}
		if total += len(content); total > api.MaxFilesBytes {
			return specFailure{ReasonFilesOverBound, fmt.Errorf("the content of spec.files is over %d bytes once resolved", api.MaxFilesBytes)}
		}
		uid, gid, err := fileOwner(spec, f)
		if err != nil {
			return err
		}
		mode := api.DefaultFileMode
		if f.Mode != nil {
			mode = *f.Mode
		}
		req.Files = append(req.Files, &v1alpha2.File{Path: f.Path, Content: content, Mode: uint32(mode), Uid: uid, Gid: gid})
	}
	return nil
}

// fileOwner is the owner of f in the guest: its own, else the MicroVM's user,
// else root. The agent cannot resolve a user name in the guest, so a file
// that gives no owner under a user that is not a numeric uid:gid is refused.
func fileOwner(spec *api.MicroVMSpec, f api.MicroVMFile) (uid, gid uint32, err error) {
	if f.Owner != nil {
		if f.Owner.UID > math.MaxUint32 || f.Owner.GID > math.MaxUint32 {
			return 0, 0, specFailure{ReasonInvalidSpec, fmt.Errorf("file %s: owner is above %d", f.Path, uint32(math.MaxUint32))}
		}
		return uint32(f.Owner.UID), uint32(f.Owner.GID), nil
	}
	if spec.User == "" {
		return 0, 0, nil
	}
	u, g, ok := strings.Cut(spec.User, ":")
	uid64, uerr := strconv.ParseUint(u, 10, 32)
	gid64, gerr := strconv.ParseUint(g, 10, 32)
	if !ok || uerr != nil || gerr != nil {
		return 0, 0, specFailure{ReasonInvalidSpec, fmt.Errorf("file %s: gives no owner and spec.user is not a numeric uid:gid, so its owner cannot be decided", f.Path)}
	}
	return uint32(uid64), uint32(gid64), nil
}

// references reads the Secrets and ConfigMaps of one MicroVM, each once.
type references struct {
	api        API
	secrets    map[string]*api.Secret
	configMaps map[string]*api.ConfigMap
}

// resolve returns the value src names. The value is a Secret's or a
// ConfigMap's and is never put in an error.
func (r *references) resolve(ctx context.Context, src *api.ValueSource) ([]byte, error) {
	if ref := src.SecretKeyRef; ref != nil {
		secret, ok := r.secrets[ref.Name]
		if !ok {
			var err error
			if secret, err = r.api.GetSecret(ctx, ref.Name); err != nil {
				return nil, unreadable("Secret", ref.Name, err)
			}
			r.secrets[ref.Name] = secret
		}
		value, ok := secret.Data[ref.Key]
		if !ok {
			return nil, specFailure{ReasonReferenceNotFound, fmt.Errorf("Secret %q has no key %q", ref.Name, ref.Key)}
		}
		return value, nil
	}
	ref := src.ConfigMapKeyRef
	configMap, ok := r.configMaps[ref.Name]
	if !ok {
		var err error
		if configMap, err = r.api.GetConfigMap(ctx, ref.Name); err != nil {
			return nil, unreadable("ConfigMap", ref.Name, err)
		}
		r.configMaps[ref.Name] = configMap
	}
	value, ok := configMap.Data[ref.Key]
	if !ok {
		return nil, specFailure{ReasonReferenceNotFound, fmt.Errorf("ConfigMap %q has no key %q", ref.Name, ref.Key)}
	}
	return []byte(value), nil
}

// unreadable is the error of a get of an object: its absence fails the
// MicroVM, and any other error is retried.
func unreadable(kind, name string, err error) error {
	if errors.Is(err, api.ErrNotFound) {
		return specFailure{ReasonReferenceNotFound, fmt.Errorf("%s %q not found", kind, name)}
	}
	return fmt.Errorf("get %s %q: %w", kind, name, err)
}

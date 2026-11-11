package resourceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// kind describes one resource the Store holds: the path segment its objects
// are served and stored under, and how to reach the metadata of one.
type kind[T any] struct {
	plural string
	meta   func(*T) *api.ObjectMeta
	spec   func(*T) any
}

func (k kind[T]) prefix() string {
	return "/registry/" + k.plural + "/"
}

func (k kind[T]) key(name string) string {
	return k.prefix() + name
}

// get returns the stored object named name, at the version it is stored at.
func (k kind[T]) get(ctx context.Context, st Store, name string) (*T, int64, error) {
	e, err := st.Get(ctx, k.key(name))
	if err != nil {
		return nil, 0, k.translate(err, name)
	}
	obj, err := k.decode(e)
	return obj, e.Revision, err
}

// decode reads the object an entry holds and sets its version to the entry's
// revision. The version is not stored with the object.
func (k kind[T]) decode(e Entry) (*T, error) {
	var obj T
	if err := json.Unmarshal(e.Value, &obj); err != nil {
		return nil, fmt.Errorf("decode %s: %w", e.Key, err)
	}
	k.meta(&obj).ResourceVersion = version(e.Revision)
	return &obj, nil
}

// encode serialises obj without its version.
func (k kind[T]) encode(obj *T) ([]byte, error) {
	m := k.meta(obj)
	rv := m.ResourceVersion
	m.ResourceVersion = ""
	defer func() { m.ResourceVersion = rv }()
	return json.Marshal(obj)
}

// create stores obj, which has no version, and sets the version it got.
func (k kind[T]) create(ctx context.Context, st Store, obj *T) error {
	value, err := k.encode(obj)
	if err != nil {
		return err
	}
	name := k.meta(obj).Name
	rev, err := st.Create(ctx, k.key(name), value)
	if err != nil {
		return k.translate(err, name)
	}
	k.meta(obj).ResourceVersion = version(rev)
	return nil
}

// update replaces the object stored at revision with obj and sets the version
// it got.
func (k kind[T]) update(ctx context.Context, st Store, obj *T, revision int64) error {
	value, err := k.encode(obj)
	if err != nil {
		return err
	}
	name := k.meta(obj).Name
	rev, err := st.Update(ctx, k.key(name), value, revision)
	if err != nil {
		return k.translate(err, name)
	}
	k.meta(obj).ResourceVersion = version(rev)
	return nil
}

// remove deletes the object stored at revision and sets obj's version to the
// revision of the deletion.
func (k kind[T]) remove(ctx context.Context, st Store, obj *T, revision int64) error {
	name := k.meta(obj).Name
	deleted, err := st.Delete(ctx, k.key(name), revision)
	if err != nil {
		return k.translate(err, name)
	}
	k.meta(obj).ResourceVersion = version(deleted.Revision)
	return nil
}

// put creates obj when it carries no version, and otherwise replaces the
// stored object obj was read at. The metadata fields only the server writes
// are set here, and onCreate and onUpdate set the rest and may refuse the
// write. onUpdate receives the stored object.
func (k kind[T]) put(ctx context.Context, st Store, obj *T, onCreate func() error, onUpdate func(old *T) error) (*T, error) {
	m := k.meta(obj)
	if m.ResourceVersion == "" {
		m.Generation, m.DeletionTimestamp = 1, nil
		if err := onCreate(); err != nil {
			return nil, err
		}
		if err := k.create(ctx, st, obj); err != nil {
			return nil, err
		}
		return obj, nil
	}
	old, rev, err := k.get(ctx, st, m.Name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(k.plural, m.Name, m.ResourceVersion, k.meta(old).ResourceVersion); err != nil {
		return nil, err
	}
	m.Generation = nextGeneration(k.meta(old).Generation, k.spec(old), k.spec(obj))
	m.DeletionTimestamp = k.meta(old).DeletionTimestamp
	if err := onUpdate(old); err != nil {
		return nil, err
	}
	if err := k.update(ctx, st, obj, rev); err != nil {
		return nil, err
	}
	return obj, nil
}

// list returns the stored objects and the version to watch from.
func (k kind[T]) list(ctx context.Context, st Store) ([]T, api.ResourceVersion, error) {
	entries, rev, err := st.List(ctx, k.prefix())
	if err != nil {
		return nil, "", err
	}
	items := make([]T, 0, len(entries))
	for _, e := range entries {
		obj, err := k.decode(e)
		if err != nil {
			return nil, "", err
		}
		items = append(items, *obj)
	}
	return items, version(rev), nil
}

// watch streams the changes made after version rv as watch events, ending
// with an Error event when the stream fails, and closes the channel after it.
func (k kind[T]) watch(ctx context.Context, st Store, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	after, err := parseVersion(rv)
	if err != nil {
		return nil, err
	}
	in, err := st.Watch(ctx, k.prefix(), after)
	if err != nil {
		return nil, err
	}
	out := make(chan api.WatchEvent)
	go func() {
		defer close(out)
		for ev := range in {
			converted := k.convert(ev)
			select {
			case out <- converted:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (k kind[T]) convert(ev Event) api.WatchEvent {
	failure := ev.Err
	if failure == nil {
		converted, err := k.event(ev)
		if err == nil {
			return converted
		}
		failure = err
	}
	apiErr := internalError(failure)
	if errors.Is(failure, ErrCompacted) {
		apiErr = api.NewAPIError(api.ReasonExpired, "the resource version is older than the Store keeps; list again")
	}
	converted, err := api.NewErrorEvent(apiErr)
	if err != nil {
		panic(err)
	}
	return converted
}

func (k kind[T]) event(ev Event) (api.WatchEvent, error) {
	obj, err := k.decode(ev.Entry)
	if err != nil {
		return api.WatchEvent{}, err
	}
	return api.NewEvent(ev.Type, obj)
}

// translate maps a Store failure on the object named name to its API error.
// Any other failure is returned as it is.
func (k kind[T]) translate(err error, name string) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return api.NewAPIError(api.ReasonNotFound, "%s %q not found", k.plural, name)
	case errors.Is(err, ErrExists):
		return api.NewAPIError(api.ReasonAlreadyExists, "%s %q already exists", k.plural, name)
	case errors.Is(err, ErrStale):
		return api.NewAPIError(api.ReasonConflict, "%s %q was changed since the version given", k.plural, name)
	default:
		return err
	}
}

// internalError is the error object for a failure that is not the caller's.
func internalError(err error) *api.APIError {
	return &api.APIError{Code: 500, Reason: ReasonInternal, Message: err.Error()}
}

// ReasonInternal is the reason of the error object for a failure of the
// server, which the contract does not name.
const ReasonInternal api.Reason = "Internal"

func version(rev int64) api.ResourceVersion {
	return api.ResourceVersion(strconv.FormatInt(rev, 10))
}

func parseVersion(rv api.ResourceVersion) (int64, error) {
	rev, err := strconv.ParseInt(string(rv), 10, 64)
	if err != nil || rev < 0 {
		return 0, api.NewAPIError(api.ReasonInvalid, "resourceVersion %q is not a version", rv)
	}
	return rev, nil
}

// checkVersion refuses a write that does not carry the version the object is
// stored at.
func checkVersion(plural, name string, given, current api.ResourceVersion) error {
	if given != current {
		return api.NewAPIError(api.ReasonConflict, "%s %q is at version %s, not %q", plural, name, current, given)
	}
	return nil
}

// checkName refuses a body whose name is not the one in the path, and a name
// that would address another key.
func checkName(pathName, bodyName string) error {
	switch {
	case bodyName != pathName:
		return api.NewAPIError(api.ReasonInvalid, "metadata.name %q differs from the path %q", bodyName, pathName)
	case strings.Contains(pathName, "/"):
		return api.NewAPIError(api.ReasonInvalid, "name %q contains a slash", pathName)
	}
	return nil
}

// nextGeneration is the generation after a write: one more when the spec
// changed.
func nextGeneration(current int64, oldSpec, newSpec any) int64 {
	if sameJSON(oldSpec, newSpec) {
		return current
	}
	return current + 1
}

func sameJSON(a, b any) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

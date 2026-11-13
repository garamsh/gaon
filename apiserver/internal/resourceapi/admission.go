package resourceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/garamsh/gaon/apiserver/internal/admission"
	"github.com/garamsh/gaon/lib/api"
)

type userKey struct{}

// WithUser returns ctx carrying the identity of the caller, which a write
// sends to the admission webhooks.
func WithUser(ctx context.Context, u api.UserInfo) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

func userOf(ctx context.Context) api.UserInfo {
	u, _ := ctx.Value(userKey{}).(api.UserInfo)
	return u
}

// webhookSource lists the webhooks the two configuration kinds hold. The Store
// lists by key, so the configurations come in the order of their names.
type webhookSource struct{ st Store }

func (w webhookSource) Mutating(ctx context.Context) ([]admission.Hook, error) {
	items, _, err := mutatingWebhookConfigurations.list(ctx, w.st, nil)
	if err != nil {
		return nil, err
	}
	var hooks []admission.Hook
	for _, c := range items {
		hooks = appendHooks(hooks, c.Metadata.Name, c.Webhooks)
	}
	return hooks, nil
}

func (w webhookSource) Validating(ctx context.Context) ([]admission.Hook, error) {
	items, _, err := validatingWebhookConfigurations.list(ctx, w.st, nil)
	if err != nil {
		return nil, err
	}
	var hooks []admission.Hook
	for _, c := range items {
		hooks = appendHooks(hooks, c.Metadata.Name, c.Webhooks)
	}
	return hooks, nil
}

func appendHooks(hooks []admission.Hook, configuration string, webhooks []api.Webhook) []admission.Hook {
	for _, w := range webhooks {
		hooks = append(hooks, admission.Hook{Configuration: configuration, Webhook: w})
	}
	return hooks
}

// operationOf is the operation of a put: a body without a version creates.
func operationOf(rv api.ResourceVersion) api.Operation {
	if rv == "" {
		return api.OperationCreate
	}
	return api.OperationUpdate
}

// request describes the write of obj to the webhooks. old is the stored object
// and is nil for a create.
func request[T any](ctx context.Context, k kind[T], sub string, op api.Operation, obj, old *T) (admission.Request, error) {
	named := obj
	if named == nil {
		named = old
	}
	m := k.meta(named)
	req := admission.Request{
		Kind:        k.gvk,
		Resource:    api.GroupVersionResource{Group: k.gvk.Group, Version: k.gvk.Version, Resource: strings.TrimSuffix(k.plural, "."+k.gvk.Group)},
		SubResource: sub,
		Operation:   op,
		Name:        m.Name,
		User:        userOf(ctx),
	}
	var err error
	if obj != nil {
		if req.Object, err = json.Marshal(obj); err != nil {
			return req, err
		}
	}
	if old != nil {
		req.OldObject, err = json.Marshal(old)
	}
	return req, err
}

func decodes[T any](raw json.RawMessage) error {
	var obj T
	return json.Unmarshal(raw, &obj)
}

// mutate sends the write of obj to the mutating webhooks and replaces *obj
// with what they leave. An update reads the stored object for the webhooks to
// see; it is read again by the write itself.
func mutate[T any](ctx context.Context, s *service, k kind[T], sub string, op api.Operation, obj *T) error {
	var old *T
	if op == api.OperationUpdate {
		var err error
		if old, _, err = k.get(ctx, s.st, k.meta(obj).Name); err != nil {
			return err
		}
	}
	return mutateAgainst(ctx, s, k, sub, op, obj, old)
}

// mutateAgainst is mutate for a caller that holds the stored object old.
func mutateAgainst[T any](ctx context.Context, s *service, k kind[T], sub string, op api.Operation, obj, old *T) error {
	req, err := request(ctx, k, sub, op, obj, old)
	if err != nil {
		return err
	}
	patched, err := s.hooks.Mutate(ctx, req, decodes[T])
	if err != nil || bytes.Equal(patched, req.Object) {
		return err
	}
	var next T
	if err := json.Unmarshal(patched, &next); err != nil {
		return err
	}
	*obj = next
	return nil
}

// validate sends the write of obj, as it is about to be stored, to the
// validating webhooks.
func validate[T any](ctx context.Context, s *service, k kind[T], sub string, op api.Operation, obj, old *T) error {
	req, err := request(ctx, k, sub, op, obj, old)
	if err != nil {
		return err
	}
	return s.hooks.Validate(ctx, req)
}

// bindingRequest describes the creation of the binding b of the microvm named
// name, which is a CREATE of microvms/binding that carries the Binding.
func bindingRequest(ctx context.Context, name string, b api.Binding) (admission.Request, error) {
	raw, err := json.Marshal(b)
	return admission.Request{
		Kind:        gvk(api.Group, "Binding"),
		Resource:    api.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: microvms.plural},
		SubResource: "binding",
		Operation:   api.OperationCreate,
		Name:        name,
		User:        userOf(ctx),
		Object:      raw,
	}, err
}

// mutateBinding sends the binding to the mutating webhooks and replaces *b
// with what they leave.
func (s *service) mutateBinding(ctx context.Context, name string, b *api.Binding) error {
	req, err := bindingRequest(ctx, name, *b)
	if err != nil {
		return err
	}
	patched, err := s.hooks.Mutate(ctx, req, decodes[api.Binding])
	if err != nil || bytes.Equal(patched, req.Object) {
		return err
	}
	var next api.Binding
	if err := json.Unmarshal(patched, &next); err != nil {
		return err
	}
	*b = next
	return nil
}

// validateBinding sends the binding to the validating webhooks.
func (s *service) validateBinding(ctx context.Context, name string, b api.Binding) error {
	req, err := bindingRequest(ctx, name, b)
	if err != nil {
		return err
	}
	return s.hooks.Validate(ctx, req)
}

// admitDelete sends the delete of the stored object old to the mutating and
// then the validating webhooks. It runs after the checks of the delete and
// before its first write.
func admitDelete[T any](ctx context.Context, s *service, k kind[T], old *T) error {
	req, err := request(ctx, k, "", api.OperationDelete, nil, old)
	if err != nil {
		return err
	}
	if _, err := s.hooks.Mutate(ctx, req, nil); err != nil {
		return err
	}
	return s.hooks.Validate(ctx, req)
}

// ensure creates obj when the Store holds none of its name, without calling a
// webhook: the write is the server's own and no caller's, so a webhook that
// cannot be reached must not stop the server. An object that exists is neither
// written nor sent to a webhook, and one created at the same moment by another
// server counts as one that was there.
func ensure[T any](ctx context.Context, s *service, k kind[T], obj *T, validate func() error) error {
	name := k.meta(obj).Name
	if _, _, err := k.get(ctx, s.st, name); err == nil {
		return nil
	} else if !errors.Is(err, api.ErrNotFound) {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	_, err := k.put(ctx, s.st, obj, func() error { return nil }, func(*T) error { return nil })
	if errors.Is(err, api.ErrAlreadyExists) {
		return nil
	}
	return err
}

// admittedPut is kind.put with the validating webhooks called on the object as
// onCreate or onUpdate leaves it, just before it is stored.
func admittedPut[T any](ctx context.Context, s *service, k kind[T], obj *T, onCreate func() error, onUpdate func(old *T) error) (*T, error) {
	return k.put(ctx, s.st, obj, func() error {
		if err := onCreate(); err != nil {
			return err
		}
		return validate(ctx, s, k, "", api.OperationCreate, obj, nil)
	}, func(old *T) error {
		if err := onUpdate(old); err != nil {
			return err
		}
		return validate(ctx, s, k, "", api.OperationUpdate, obj, old)
	})
}

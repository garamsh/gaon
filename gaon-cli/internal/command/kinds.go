package command

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// result is what a read verb prints: the object as the API returned it, the
// same content as table rows, and what describe prints of one object. A list
// has no meta, details or conditions.
type result struct {
	object any
	header []string
	rows   [][]string

	meta api.ObjectMeta
	// details are the labelled lines describe prints after the metadata.
	details []detail
	// conditions are the status conditions describe prints when withConditions
	// is set; a kind whose status has none leaves it false.
	conditions     []api.Condition
	withConditions bool
}

// detail is one labelled line of describe.
type detail struct{ label, value string }

// change is one resource of an apply file, checked and ready to send.
type change struct {
	name string
	// version is the resourceVersion the file carries; empty when it carries none.
	version api.ResourceVersion
	current func(ctx context.Context, c *api.Client) (api.ResourceVersion, error)
	send    func(ctx context.Context, c *api.Client, version api.ResourceVersion) error
}

// kind is one resource kind and the verbs the API has a path for. A verb
// without a path is nil, and the command refuses it locally. A kind is named
// by its name, its plural or its short name.
type kind struct {
	name, plural, short string
	apiKind             string
	collection          string
	get                 func(ctx context.Context, c *api.Client, name string) (result, error)
	list                func(ctx context.Context, c *api.Client) (result, error)
	del                 func(ctx context.Context, c *api.Client, name string) (marked bool, err error)
	parse               func(doc []byte) (change, error)
}

// kinds lists a kind for every resource type of lib/api.
var kinds = []kind{
	spec[api.Machine, api.MachineList]{
		name:       "machine",
		plural:     "machines",
		short:      "mc",
		apiKind:    api.KindMachine,
		collection: "/machines",
		header:     []string{"NAME", "PHASE", "NODE", "EPOCH"},
		row: func(m *api.Machine) []string {
			return []string{m.Metadata.Name, string(m.EffectivePhase()), orDash(m.Spec.NodeName), strconv.FormatInt(int64(m.Spec.BindingEpoch), 10)}
		},
		details: func(m *api.Machine) []detail {
			return []detail{
				{"Image", orDash(m.Spec.Image)},
				{"Kernel", orDash(m.Spec.Kernel)},
				{"Run strategy", string(m.Spec.RunStrategy)},
				{"Node", orDash(m.Spec.NodeName)},
				{"Binding epoch", strconv.FormatInt(int64(m.Spec.BindingEpoch), 10)},
				{"Phase", string(m.EffectivePhase())},
				{"Requests", quantities(named("cpu", m.Spec.Resources.Requests.CPU), named("memory", m.Spec.Resources.Requests.Memory))},
				{"Limits", quantities(named("cpu", m.Spec.Resources.Limits.CPU), named("memory", m.Spec.Resources.Limits.Memory))},
				{"Volumes", joinOrDash(volumeMounts(m.Spec.Volumes))},
				{"Networks", joinOrDash(m.Spec.Networks)},
				{"Ports", joinOrDash(ports(m.Spec.Ports))},
				{"Addresses", joinOrDash(m.Status.Addresses)},
				{"Restart count", strconv.FormatInt(int64(m.Status.RestartCount), 10)},
				{"Last exit", lastExit(m.Status.LastExit)},
			}
		},
		conditions: func(m *api.Machine) []api.Condition { return m.Status.Conditions },
		items:      func(l *api.MachineList) []api.Machine { return l.Items },
		meta:       func(m *api.Machine) *api.ObjectMeta { return &m.Metadata },
		validate:   (*api.Machine).Validate,
		get:        (*api.Client).GetMachine,
		list:       (*api.Client).ListMachines,
		put:        (*api.Client).PutMachine,
		del:        (*api.Client).DeleteMachine,
	}.kind(),
	spec[api.Node, api.NodeList]{
		name:       "node",
		plural:     "nodes",
		short:      "no",
		apiKind:    api.KindNode,
		collection: "/nodes",
		header:     []string{"NAME", "STATE", "ADMITTED"},
		row: func(n *api.Node) []string {
			return []string{n.Metadata.Name, orDash(string(n.Status.State)), strconv.FormatBool(n.Spec.Admitted)}
		},
		details: func(n *api.Node) []detail {
			return []detail{
				{"Admitted", strconv.FormatBool(n.Spec.Admitted)},
				{"State", orDash(string(n.Status.State))},
				{"Capacity", quantities(named("cpu", n.Status.Capacity.CPU), named("memory", n.Status.Capacity.Memory), named("storage", n.Status.Capacity.Storage))},
				{"Addresses", joinOrDash(n.Status.Addresses)},
			}
		},
		conditions: func(n *api.Node) []api.Condition { return n.Status.Conditions },
		items:      func(l *api.NodeList) []api.Node { return l.Items },
		meta:       func(n *api.Node) *api.ObjectMeta { return &n.Metadata },
		validate:   (*api.Node).Validate,
		get:        (*api.Client).GetNode,
		list:       (*api.Client).ListNodes,
		put:        (*api.Client).PutNode,
	}.kind(),
	spec[api.Lease, api.LeaseList]{
		name:       "lease",
		plural:     "leases",
		short:      "le",
		apiKind:    api.KindLease,
		collection: "/leases",
		header:     []string{"NAME", "HOLDER", "DURATION"},
		row: func(l *api.Lease) []string {
			return []string{l.Metadata.Name, orDash(l.Spec.Holder), fmt.Sprintf("%ds", l.Spec.DurationSeconds)}
		},
		details: func(l *api.Lease) []detail {
			return []detail{
				{"Holder", orDash(l.Spec.Holder)},
				{"Renew time", l.Spec.RenewTime.Format(time.RFC3339)},
				{"Duration", fmt.Sprintf("%ds", l.Spec.DurationSeconds)},
			}
		},
		items:    func(l *api.LeaseList) []api.Lease { return l.Items },
		meta:     func(l *api.Lease) *api.ObjectMeta { return &l.Metadata },
		validate: (*api.Lease).Validate,
		get:      (*api.Client).GetLease,
		list:     (*api.Client).ListLeases,
		put:      (*api.Client).PutLease,
	}.kind(),
	spec[api.Volume, api.VolumeList]{
		name:       "volume",
		plural:     "volumes",
		short:      "vol",
		apiKind:    api.KindVolume,
		collection: "/volumes",
		header:     []string{"NAME", "CLASS", "SIZE", "PHASE", "NODE"},
		row: func(v *api.Volume) []string {
			return []string{v.Metadata.Name, v.Spec.StorageClass, orDash(string(v.Spec.Storage)), orDash(string(v.Status.Phase)), orDash(v.Spec.NodeName)}
		},
		details: func(v *api.Volume) []detail {
			return []detail{
				{"Storage class", v.Spec.StorageClass},
				{"Access modes", joinOrDash(v.Spec.AccessModes)},
				{"Storage", orDash(string(v.Spec.Storage))},
				{"Data source", orDash(v.Spec.DataSource)},
				{"Node", orDash(v.Spec.NodeName)},
				{"Phase", orDash(string(v.Status.Phase))},
				{"Capacity", orDash(string(v.Status.Capacity))},
				{"Attached to", attachment(v.Status.Attachment)},
			}
		},
		items:    func(l *api.VolumeList) []api.Volume { return l.Items },
		meta:     func(v *api.Volume) *api.ObjectMeta { return &v.Metadata },
		validate: (*api.Volume).Validate,
		get:      (*api.Client).GetVolume,
		list:     (*api.Client).ListVolumes,
		put:      (*api.Client).PutVolume,
		del:      (*api.Client).DeleteVolume,
	}.kind(),
}

// spec describes one kind in terms of its types and the client methods that
// have a path; a method the API has no path for is left nil.
type spec[T, L any] struct {
	name, plural, short string
	apiKind, collection string
	header              []string
	row                 func(*T) []string
	details             func(*T) []detail
	conditions          func(*T) []api.Condition // nil for a kind whose status has none
	items               func(*L) []T
	meta                func(*T) *api.ObjectMeta
	validate            func(*T) error
	get                 func(*api.Client, context.Context, string) (*T, error)
	list                func(*api.Client, context.Context) (*L, error)
	put                 func(*api.Client, context.Context, *T) (*T, error)
	del                 func(*api.Client, context.Context, string, api.ResourceVersion) (*T, error)
}

func (s spec[T, L]) kind() kind {
	k := kind{name: s.name, plural: s.plural, short: s.short, apiKind: s.apiKind, collection: s.collection}
	if s.get != nil {
		k.get = func(ctx context.Context, c *api.Client, name string) (result, error) {
			obj, err := s.get(c, ctx, name)
			if err != nil {
				return result{}, err
			}
			res := result{object: obj, header: s.header, rows: [][]string{s.row(obj)}, meta: *s.meta(obj), details: s.details(obj)}
			if s.conditions != nil {
				res.conditions, res.withConditions = s.conditions(obj), true
			}
			return res, nil
		}
	}
	if s.list != nil {
		k.list = func(ctx context.Context, c *api.Client) (result, error) {
			list, err := s.list(c, ctx)
			if err != nil {
				return result{}, err
			}
			res := result{object: list, header: s.header}
			items := s.items(list)
			for i := range items {
				res.rows = append(res.rows, s.row(&items[i]))
			}
			return res, nil
		}
	}
	if s.del != nil {
		k.del = func(ctx context.Context, c *api.Client, name string) (bool, error) {
			cur, err := s.get(c, ctx, name)
			if err != nil {
				return false, err
			}
			res, err := s.del(c, ctx, name, s.meta(cur).ResourceVersion)
			if err != nil {
				return false, err
			}
			// The answer shows which: an object that is still held by a
			// finalizer comes back marked, any other has been removed.
			m := s.meta(res)
			return m.DeletionTimestamp != nil && len(m.Finalizers) > 0, nil
		}
	}
	if s.put != nil {
		k.parse = func(doc []byte) (change, error) {
			obj := new(T)
			if err := decodeStrict(doc, obj); err != nil {
				return change{}, err
			}
			if err := s.validate(obj); err != nil {
				return change{}, err
			}
			meta := s.meta(obj)
			return change{
				name:    meta.Name,
				version: meta.ResourceVersion,
				current: func(ctx context.Context, c *api.Client) (api.ResourceVersion, error) {
					cur, err := s.get(c, ctx, meta.Name)
					if err != nil {
						return "", err
					}
					return s.meta(cur).ResourceVersion, nil
				},
				send: func(ctx context.Context, c *api.Client, version api.ResourceVersion) error {
					meta.ResourceVersion = version
					_, err := s.put(c, ctx, obj)
					return err
				},
			}, nil
		}
	}
	return k
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// kindByName finds the kind a name stands for: its name, plural or short name.
func kindByName(name string) (kind, bool) {
	for _, k := range kinds {
		if name == k.name || name == k.plural || name == k.short {
			return k, true
		}
	}
	return kind{}, false
}

func kindByAPIKind(apiKind string) (kind, bool) {
	for _, k := range kinds {
		if k.apiKind == apiKind {
			return k, true
		}
	}
	return kind{}, false
}

// kindSpellings lists each kind with its other names, for messages.
func kindSpellings() []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = fmt.Sprintf("%s (%s, %s)", k.name, k.plural, k.short)
	}
	return out
}

// refusal is the error for a verb the API has no path for. Nothing is sent.
func refusal(verb string, k kind, method, path string) error {
	return fmt.Errorf("%s %s: refused, the resource API has no path %s %s%s", verb, k.name, method, k.collection, path)
}

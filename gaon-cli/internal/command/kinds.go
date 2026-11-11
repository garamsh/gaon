package command

import (
	"context"
	"fmt"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
)

// result is what a read verb prints: the object as the API returned it, and
// the same content as table rows.
type result struct {
	object any
	header []string
	rows   [][]string
}

// change is one resource of an apply file, checked and ready to send.
type change struct {
	name string
	// version is the resourceVersion the file carries; empty when it carries none.
	version api.ResourceVersion
	current func(ctx context.Context, c *api.Client) (api.ResourceVersion, error)
	send    func(ctx context.Context, c *api.Client, version api.ResourceVersion) error
}

// kind is one resource kind and the verbs the API has a path for. A verb
// without a path is nil, and the command refuses it locally.
type kind struct {
	name       string
	apiKind    string
	collection string
	get        func(ctx context.Context, c *api.Client, name string) (result, error)
	list       func(ctx context.Context, c *api.Client) (result, error)
	del        func(ctx context.Context, c *api.Client, name string) error
	parse      func(doc []byte) (change, error)
}

// kinds lists a kind for every resource type of lib/api.
var kinds = []kind{
	spec[api.Machine, api.MachineList]{
		name:       "machine",
		apiKind:    api.KindMachine,
		collection: "/machines",
		header:     []string{"NAME", "PHASE", "NODE", "EPOCH"},
		row: func(m *api.Machine) []string {
			return []string{m.Metadata.Name, string(m.EffectivePhase()), orDash(m.Spec.NodeName), strconv.FormatInt(int64(m.Spec.BindingEpoch), 10)}
		},
		items:    func(l *api.MachineList) []api.Machine { return l.Items },
		meta:     func(m *api.Machine) *api.ObjectMeta { return &m.Metadata },
		validate: (*api.Machine).Validate,
		get:      (*api.Client).GetMachine,
		list:     (*api.Client).ListMachines,
		put:      (*api.Client).PutMachine,
		del:      (*api.Client).DeleteMachine,
	}.kind(),
	spec[api.Node, api.NodeList]{
		name:       "node",
		apiKind:    api.KindNode,
		collection: "/nodes",
		header:     []string{"NAME", "STATE", "ADMITTED"},
		row: func(n *api.Node) []string {
			return []string{n.Metadata.Name, orDash(string(n.Status.State)), strconv.FormatBool(n.Spec.Admitted)}
		},
		items:    func(l *api.NodeList) []api.Node { return l.Items },
		meta:     func(n *api.Node) *api.ObjectMeta { return &n.Metadata },
		validate: (*api.Node).Validate,
		get:      (*api.Client).GetNode,
		list:     (*api.Client).ListNodes,
		put:      (*api.Client).PutNode,
	}.kind(),
	spec[api.Lease, api.LeaseList]{
		name:       "lease",
		apiKind:    api.KindLease,
		collection: "/leases",
		header:     []string{"NAME", "HOLDER", "DURATION"},
		row: func(l *api.Lease) []string {
			return []string{l.Metadata.Name, orDash(l.Spec.Holder), fmt.Sprintf("%ds", l.Spec.DurationSeconds)}
		},
		items:    func(l *api.LeaseList) []api.Lease { return l.Items },
		meta:     func(l *api.Lease) *api.ObjectMeta { return &l.Metadata },
		validate: (*api.Lease).Validate,
		get:      (*api.Client).GetLease,
		list:     (*api.Client).ListLeases,
		put:      (*api.Client).PutLease,
	}.kind(),
	// Volume has only a status path, which no verb uses.
	{name: "volume", apiKind: api.KindVolume, collection: "/volumes"},
}

// spec describes one kind in terms of its types and the client methods that
// have a path; a method the API has no path for is left nil.
type spec[T, L any] struct {
	name, apiKind, collection string
	header                    []string
	row                       func(*T) []string
	items                     func(*L) []T
	meta                      func(*T) *api.ObjectMeta
	validate                  func(*T) error
	get                       func(*api.Client, context.Context, string) (*T, error)
	list                      func(*api.Client, context.Context) (*L, error)
	put                       func(*api.Client, context.Context, *T) (*T, error)
	del                       func(*api.Client, context.Context, string, api.ResourceVersion) (*T, error)
}

func (s spec[T, L]) kind() kind {
	k := kind{name: s.name, apiKind: s.apiKind, collection: s.collection}
	if s.get != nil {
		k.get = func(ctx context.Context, c *api.Client, name string) (result, error) {
			obj, err := s.get(c, ctx, name)
			if err != nil {
				return result{}, err
			}
			return result{object: obj, header: s.header, rows: [][]string{s.row(obj)}}, nil
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
		k.del = func(ctx context.Context, c *api.Client, name string) error {
			cur, err := s.get(c, ctx, name)
			if err != nil {
				return err
			}
			_, err = s.del(c, ctx, name, s.meta(cur).ResourceVersion)
			return err
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

func kindByName(name string) (kind, bool) {
	for _, k := range kinds {
		if k.name == name {
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

// kindNames lists the names of kinds, for messages.
func kindNames() []string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.name
	}
	return names
}

// refusal is the error for a verb the API has no path for. Nothing is sent.
func refusal(verb string, k kind, method, path string) error {
	return fmt.Errorf("%s %s: refused, the resource API has no path %s %s%s", verb, k.name, method, k.collection, path)
}

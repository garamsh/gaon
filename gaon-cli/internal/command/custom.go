package command

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

// resolveKind finds the kind an operand names: a built-in kind by its name,
// plural or short name, else a registered kind by its singular, its plural or
// its definition's name, <plural>.<group>, among the definitions the API
// server serves. A built-in name wins over a registered one.
func resolveKind(ctx context.Context, cl *cluster, operand string) (kind, error) {
	name := strings.ToLower(operand)
	if k, ok := kindByName(name); ok {
		return k, nil
	}
	defs, err := cl.established(ctx)
	var u *usageError
	switch {
	case errors.As(err, &u):
		// --server has no form the definitions' root can be derived from.
		return kind{}, usagef("unknown kind %q, want one of %s; registered kinds are not looked up: %v", operand, strings.Join(kindSpellings(kinds), ", "), u)
	case err != nil:
		return kind{}, fmt.Errorf("find kind %q among the definitions: %w", operand, err)
	}
	var found []kind
	for _, d := range defs {
		n := d.Spec.Names
		if name == n.Singular || name == n.Plural || name == strings.ToLower(d.Metadata.Name) {
			found = append(found, customKind(cl, d))
		}
	}
	switch len(found) {
	case 0:
		return kind{}, usagef("unknown kind %q, want one of %s", operand, strings.Join(kindSpellings(kinds, registeredKinds(cl, defs)), ", "))
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, k := range found {
		names[i] = k.plural + "." + k.group
	}
	return kind{}, usagef("kind %q is ambiguous, name one of %s", operand, strings.Join(names, ", "))
}

// resolveDocument finds the kind of an apply document: a built-in kind by its
// apiVersion and kind, else a registered kind by its group, version and kind.
func resolveDocument(ctx context.Context, cl *cluster, d document) (kind, bool, error) {
	if k, ok := kindByAPI(d.apiVersion, d.apiKind); ok {
		return k, true, nil
	}
	defs, err := cl.established(ctx)
	if err != nil {
		return kind{}, false, fmt.Errorf("find kind %q among the definitions: %w", d.apiKind, err)
	}
	for _, k := range registeredKinds(cl, defs) {
		if k.apiVersion == d.apiVersion && k.apiKind == d.apiKind {
			return k, true, nil
		}
	}
	return kind{}, false, nil
}

func registeredKinds(cl *cluster, defs []api.CustomResourceDefinition) []kind {
	out := make([]kind, len(defs))
	for i, d := range defs {
		out[i] = customKind(cl, d)
	}
	return out
}

// customKind makes the kind a definition registers. It has no short name, as
// a definition has none.
func customKind(cl *cluster, d api.CustomResourceDefinition) kind {
	version := ""
	if len(d.Spec.Versions) > 0 {
		version = d.Spec.Versions[0].Name
	}
	t := api.CustomResourceType{Group: d.Spec.Group, Version: version, Kind: d.Spec.Names.Kind, Plural: d.Spec.Names.Plural}
	client := func() (*api.CustomResourceClient, error) {
		base, err := cl.base()
		if err != nil {
			return nil, err
		}
		return api.NewCustomResourceClient(base, cl.http, t)
	}
	read := func(ctx context.Context, name string) (*api.CustomObject, error) {
		c, err := client()
		if err != nil {
			return nil, err
		}
		return c.Get(ctx, name)
	}
	header := []string{"NAME", "GENERATION"}
	row := func(o *api.CustomObject) []string {
		return []string{o.Metadata.Name, strconv.FormatInt(o.Metadata.Generation, 10)}
	}
	return kind{
		name:       d.Spec.Names.Singular,
		plural:     d.Spec.Names.Plural,
		group:      t.Group,
		apiVersion: t.Group + "/" + t.Version,
		apiKind:    t.Kind,
		collection: "/" + t.Plural,
		get: func(ctx context.Context, _ *cluster, name string) (result, error) {
			o, err := read(ctx, name)
			if err != nil {
				return result{}, err
			}
			return result{
				object: o, header: header, rows: [][]string{row(o)}, meta: o.Metadata,
				details: []detail{{"Spec", compact(o.Spec)}, {"Status", compact(o.Status)}},
			}, nil
		},
		list: func(ctx context.Context, _ *cluster) (result, error) {
			c, err := client()
			if err != nil {
				return result{}, err
			}
			list, err := c.List(ctx)
			if err != nil {
				return result{}, err
			}
			res := result{object: list, header: header}
			for i := range list.Items {
				res.rows = append(res.rows, row(&list.Items[i]))
			}
			return res, nil
		},
		del: func(ctx context.Context, _ *cluster, name string) (bool, error) {
			cur, err := read(ctx, name)
			if err != nil {
				return false, err
			}
			c, err := client()
			if err != nil {
				return false, err
			}
			res, err := c.Delete(ctx, name, cur.Metadata.ResourceVersion)
			if err != nil {
				return false, err
			}
			return res.Metadata.DeletionTimestamp != nil && len(res.Metadata.Finalizers) > 0, nil
		},
		parse: func(doc []byte) (change, error) {
			obj := new(api.CustomObject)
			if err := decodeStrict(doc, obj); err != nil {
				return change{}, err
			}
			if err := obj.Validate(t); err != nil {
				return change{}, err
			}
			return change{
				name:    obj.Metadata.Name,
				version: obj.Metadata.ResourceVersion,
				current: func(ctx context.Context, _ *cluster) (api.ResourceVersion, error) {
					cur, err := read(ctx, obj.Metadata.Name)
					if err != nil {
						return "", err
					}
					return cur.Metadata.ResourceVersion, nil
				},
				send: func(ctx context.Context, _ *cluster, version api.ResourceVersion) error {
					c, err := client()
					if err != nil {
						return err
					}
					obj.Metadata.ResourceVersion = version
					_, err = c.Put(ctx, obj)
					return err
				},
			}, nil
		},
	}
}

// compact renders a JSON object on one line, or - when there is none.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if len(raw) == 0 || json.Compact(&b, raw) != nil {
		return orDash(string(raw))
	}
	return b.String()
}

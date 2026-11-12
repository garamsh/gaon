package resourceapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// fitSection prunes and validates one section of a custom object, "spec" or
// "status", against the root of its definition's schema. A section the root
// does not name is dropped, and an absent one is an error where the root
// requires it.
func fitSection(root *api.JSONSchemaProps, section string, raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		if slices.Contains(root.Required, section) {
			return nil, api.NewAPIError(api.ReasonInvalid, "%s is required", section)
		}
		return nil, nil
	}
	child, named := root.Properties[section]
	if !named {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s: %v", section, err)
	}
	pruned, err := fit(&child, v, section)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pruned)
}

// fit returns v with the fields s does not name dropped, or the first way v
// fails s.
func fit(s *api.JSONSchemaProps, v any, path string) (any, error) {
	switch s.Type {
	case api.SchemaObject:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, mismatch(path, "an object", v)
		}
		return fitObject(s, m, path)
	case "":
		// Only a node that preserves unknown fields has no type.
		if m, ok := v.(map[string]any); ok {
			return fitObject(s, m, path)
		}
		return v, nil
	case api.SchemaArray:
		items, ok := v.([]any)
		if !ok {
			return nil, mismatch(path, "an array", v)
		}
		out := make([]any, len(items))
		for i, item := range items {
			fitted, err := fit(s.Items, item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = fitted
		}
		return out, nil
	case api.SchemaString:
		str, ok := v.(string)
		if !ok {
			return nil, mismatch(path, "a string", v)
		}
		if s.Pattern != "" {
			matched, err := regexp.MatchString(s.Pattern, str)
			if err != nil {
				return nil, err
			}
			if !matched {
				return nil, api.NewAPIError(api.ReasonInvalid, "%s does not match the pattern %q", path, s.Pattern)
			}
		}
	case api.SchemaInteger, api.SchemaNumber:
		return fitNumber(s, v, path)
	case api.SchemaBoolean:
		if _, ok := v.(bool); !ok {
			return nil, mismatch(path, "a boolean", v)
		}
	}
	if !inEnum(s.Enum, v) {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s is not one of the values the schema allows", path)
	}
	return v, nil
}

func fitObject(s *api.JSONSchemaProps, m map[string]any, path string) (any, error) {
	out := make(map[string]any, len(m))
	for name, val := range m {
		child, named := s.Properties[name]
		switch {
		case named && val == nil:
			// null is an absent field.
		case named:
			fitted, err := fit(&child, val, path+"."+name)
			if err != nil {
				return nil, err
			}
			out[name] = fitted
		case s.PreserveUnknownFields:
			out[name] = val
		}
	}
	for _, name := range s.Required {
		if _, ok := out[name]; !ok {
			return nil, api.NewAPIError(api.ReasonInvalid, "%s.%s is required", path, name)
		}
	}
	return out, nil
}

func fitNumber(s *api.JSONSchemaProps, v any, path string) (any, error) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, mismatch(path, "a number", v)
	}
	f, err := n.Float64()
	if err != nil {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s is %s, which is out of range", path, n)
	}
	if s.Type == api.SchemaInteger && f != math.Trunc(f) {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s is %s, want an integer", path, n)
	}
	if s.Minimum != nil && f < *s.Minimum {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s is %s, below the minimum %v", path, n, *s.Minimum)
	}
	if s.Maximum != nil && f > *s.Maximum {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s is %s, above the maximum %v", path, n, *s.Maximum)
	}
	if !inEnum(s.Enum, v) {
		return nil, api.NewAPIError(api.ReasonInvalid, "%s is not one of the values the schema allows", path)
	}
	return v, nil
}

// inEnum reports whether v is one of the values of enum, which is empty where
// the node has none.
func inEnum(enum []json.RawMessage, v any) bool {
	if len(enum) == 0 {
		return true
	}
	for _, raw := range enum {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var allowed any
		if dec.Decode(&allowed) == nil && sameScalar(allowed, v) {
			return true
		}
	}
	return false
}

func sameScalar(a, b any) bool {
	an, aNumber := a.(json.Number)
	bn, bNumber := b.(json.Number)
	if aNumber && bNumber {
		af, errA := an.Float64()
		bf, errB := bn.Float64()
		return errA == nil && errB == nil && af == bf
	}
	return a == b
}

func mismatch(path, want string, got any) error {
	return api.NewAPIError(api.ReasonInvalid, "%s is %s, want %s", path, describe(got), want)
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	}
	return strings.TrimSpace(fmt.Sprintf("%T", v))
}

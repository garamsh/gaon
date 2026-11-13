package api

import (
	"encoding/json"
	"regexp"
	"sort"
)

// SchemaType is the type of a schema node.
type SchemaType string

// The types a schema node takes.
const (
	SchemaObject  SchemaType = "object"
	SchemaArray   SchemaType = "array"
	SchemaString  SchemaType = "string"
	SchemaInteger SchemaType = "integer"
	SchemaNumber  SchemaType = "number"
	SchemaBoolean SchemaType = "boolean"
)

// JSONSchemaProps is a node of a structural subset of OpenAPI v3. Decoding
// records any keyword outside the subset, and validation refuses a schema
// that has one.
type JSONSchemaProps struct {
	Type        SchemaType                 `json:"type,omitempty"`
	Description string                     `json:"description,omitempty"`
	Properties  map[string]JSONSchemaProps `json:"properties,omitempty"`
	Required    []string                   `json:"required,omitempty"`
	Items       *JSONSchemaProps           `json:"items,omitempty"`
	Enum        []json.RawMessage          `json:"enum,omitempty"`
	Minimum     *float64                   `json:"minimum,omitempty"`
	Maximum     *float64                   `json:"maximum,omitempty"`
	Pattern     string                     `json:"pattern,omitempty"`
	// PreserveUnknownFields keeps the fields of an object that the schema does
	// not name, and lets the node omit its type.
	PreserveUnknownFields bool `json:"x-kubernetes-preserve-unknown-fields,omitempty"`
	// IntOrString lets the node hold an integer or a string, and takes no type.
	// Pattern and Enum apply to the string; an integer must be one of Enum.
	IntOrString bool `json:"x-kubernetes-int-or-string,omitempty"`

	unsupported []string
}

var supportedKeywords = map[string]bool{
	"type": true, "description": true, "properties": true, "required": true, "items": true,
	"enum": true, "minimum": true, "maximum": true, "pattern": true,
	"x-kubernetes-preserve-unknown-fields": true, "x-kubernetes-int-or-string": true,
}

// UnmarshalJSON decodes a node and records the keywords outside the subset.
func (s *JSONSchemaProps) UnmarshalJSON(data []byte) error {
	type plain JSONSchemaProps
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	for k := range keys {
		if !supportedKeywords[k] {
			p.unsupported = append(p.unsupported, k)
		}
	}
	sort.Strings(p.unsupported)
	*s = JSONSchemaProps(p)
	return nil
}

// validateRoot checks the root of a definition's schema: an object that names
// only spec and status and keeps no unknown field.
func (s *JSONSchemaProps) validateRoot() error {
	if s.Type != SchemaObject {
		return invalid("schema root has type %q, want object", s.Type)
	}
	if s.PreserveUnknownFields {
		return invalid("schema root sets x-kubernetes-preserve-unknown-fields; an object has only spec and status")
	}
	for name := range s.Properties {
		if name != "spec" && name != "status" {
			return invalid("schema root names property %q, want only spec and status", name)
		}
	}
	return s.validate("schema")
}

func (s *JSONSchemaProps) validate(path string) error {
	if len(s.unsupported) > 0 {
		return invalid("%s uses unsupported keyword %q", path, s.unsupported[0])
	}
	if s.IntOrString {
		return s.validateIntOrString(path)
	}
	switch s.Type {
	case SchemaObject, SchemaArray, SchemaString, SchemaInteger, SchemaNumber, SchemaBoolean:
	case "":
		if !s.PreserveUnknownFields {
			return invalid("%s has no type", path)
		}
	default:
		return invalid("%s has type %q, want object, array, string, integer, number or boolean", path, s.Type)
	}
	if err := s.validateObject(path); err != nil {
		return err
	}
	if err := s.validateScalar(path); err != nil {
		return err
	}
	if s.Type == SchemaArray && s.Items == nil {
		return invalid("%s is an array with no items", path)
	}
	if s.Items != nil {
		if s.Type != SchemaArray {
			return invalid("%s has items and is not an array", path)
		}
		return s.Items.validate(path + ".items")
	}
	return nil
}

// validateIntOrString checks a node that holds an integer or a string. It has
// no type and none of the keywords of an object, an array or a number.
func (s *JSONSchemaProps) validateIntOrString(path string) error {
	switch {
	case s.Type != "":
		return invalid("%s sets x-kubernetes-int-or-string and type %q; it takes no type", path, s.Type)
	case s.PreserveUnknownFields:
		return invalid("%s sets x-kubernetes-int-or-string and x-kubernetes-preserve-unknown-fields", path)
	case len(s.Properties) > 0 || len(s.Required) > 0 || s.Items != nil:
		return invalid("%s sets x-kubernetes-int-or-string and has properties, required or items", path)
	case s.Minimum != nil || s.Maximum != nil:
		return invalid("%s sets x-kubernetes-int-or-string and has minimum or maximum", path)
	}
	for _, v := range s.Enum {
		if !enumValueHasType(v, SchemaString) && !enumValueHasType(v, SchemaInteger) {
			return invalid("%s has enum value %s that is neither an integer nor a string", path, v)
		}
	}
	if s.Pattern != "" {
		if _, err := regexp.Compile(s.Pattern); err != nil {
			return invalid("%s has a pattern that does not compile: %v", path, err)
		}
	}
	return nil
}

func (s *JSONSchemaProps) validateObject(path string) error {
	if (len(s.Properties) > 0 || len(s.Required) > 0) && s.Type != SchemaObject && s.Type != "" {
		return invalid("%s has properties or required and is not an object", path)
	}
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			return invalid("%s names an empty property", path)
		}
		child := s.Properties[name]
		if err := child.validate(path + ".properties." + name); err != nil {
			return err
		}
	}
	for _, name := range s.Required {
		if _, ok := s.Properties[name]; !ok && !s.PreserveUnknownFields {
			return invalid("%s requires %q, which its properties do not name and a write prunes", path, name)
		}
	}
	return nil
}

func (s *JSONSchemaProps) validateScalar(path string) error {
	numeric := s.Type == SchemaInteger || s.Type == SchemaNumber
	if len(s.Enum) > 0 {
		if s.Type != SchemaString && s.Type != SchemaBoolean && !numeric {
			return invalid("%s has enum, which only a scalar type takes", path)
		}
		for _, v := range s.Enum {
			if !enumValueHasType(v, s.Type) {
				return invalid("%s has enum value %s that is not of type %s", path, v, s.Type)
			}
		}
	}
	if (s.Minimum != nil || s.Maximum != nil) && !numeric {
		return invalid("%s has minimum or maximum and is not a number", path)
	}
	if s.Minimum != nil && s.Maximum != nil && *s.Minimum > *s.Maximum {
		return invalid("%s has minimum %v above maximum %v", path, *s.Minimum, *s.Maximum)
	}
	if s.Pattern != "" {
		if s.Type != SchemaString {
			return invalid("%s has a pattern and is not a string", path)
		}
		if _, err := regexp.Compile(s.Pattern); err != nil {
			return invalid("%s has a pattern that does not compile: %v", path, err)
		}
	}
	return nil
}

func enumValueHasType(raw json.RawMessage, t SchemaType) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch t {
	case SchemaString:
		_, ok := v.(string)
		return ok
	case SchemaBoolean:
		_, ok := v.(bool)
		return ok
	case SchemaNumber:
		_, ok := v.(float64)
		return ok
	case SchemaInteger:
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	}
	return false
}

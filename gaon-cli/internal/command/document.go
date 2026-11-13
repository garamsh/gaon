package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"go.yaml.in/yaml/v3"

	"github.com/garamsh/gaon/lib/api"
)

// document is one YAML document of an apply file, as JSON.
type document struct {
	apiVersion string
	apiKind    string
	json       []byte
}

// parseDocuments splits a YAML stream into its documents. An empty document
// is skipped.
func parseDocuments(data []byte) ([]document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []document
	for n := 1; ; n++ {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", n, err)
		}
		if v == nil {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", n, err)
		}
		var t api.TypeMeta
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("document %d: want a mapping with apiVersion and kind: %w", n, err)
		}
		docs = append(docs, document{apiVersion: t.APIVersion, apiKind: t.Kind, json: raw})
	}
}

// decodeStrict decodes doc into out and rejects a field out has no place for.
func decodeStrict(doc []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// encodeYAML renders v as YAML through its JSON form, so field names are the
// API's.
func encodeYAML(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return yaml.Marshal(generic)
}

// holds reports whether have already holds what want says: the two are equal
// in every field a write sets. The status and the metadata fields only the
// server writes are left out.
func holds(have, want any) bool {
	h, w := writable(have), writable(want)
	return h != nil && reflect.DeepEqual(h, w)
}

// writable is v's JSON form without the status and the server-set metadata.
// A value that cannot be encoded has no form, so nothing holds it.
func writable(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	delete(m, "status")
	if meta, ok := m["metadata"].(map[string]any); ok {
		for _, f := range []string{"uid", "resourceVersion", "generation", "deletionTimestamp"} {
			delete(meta, f)
		}
	}
	return m
}

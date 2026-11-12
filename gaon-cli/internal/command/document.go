package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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

package admission

import (
	"encoding/json"
	"errors"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

// applyPatch applies the RFC 6902 patch to obj. The result must still be the
// same object: apiVersion, kind, metadata.name and metadata.resourceVersion
// are what the write was addressed by, and may not change. check, when not
// nil, refuses a result that is not a usable object of the kind.
func applyPatch(obj, patch []byte, check func(json.RawMessage) error) (json.RawMessage, error) {
	ops, err := jsonpatch.DecodePatch(patch)
	if err != nil {
		return nil, fmt.Errorf("decode patch: %w", err)
	}
	patched, err := ops.Apply(obj)
	if err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}
	before, err := identityOf(obj)
	if err != nil {
		return nil, err
	}
	after, err := identityOf(patched)
	if err != nil {
		return nil, fmt.Errorf("patched object: %w", err)
	}
	if before != after {
		return nil, errors.New("the patch changes apiVersion, kind, metadata.name or metadata.resourceVersion")
	}
	if check != nil {
		if err := check(patched); err != nil {
			return nil, fmt.Errorf("patched object: %w", err)
		}
	}
	return patched, nil
}

// identity is what a patch may not change.
type identity struct {
	APIVersion, Kind, Name, ResourceVersion string
}

func identityOf(obj []byte) (identity, error) {
	var o struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name            string `json:"name"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &o); err != nil {
		return identity{}, fmt.Errorf("decode object: %w", err)
	}
	return identity{o.APIVersion, o.Kind, o.Metadata.Name, o.Metadata.ResourceVersion}, nil
}

package resourceapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// coreGroups are the groups whose objects are stored under their plural
// alone. Any other group is a custom resource's or the coordination group's,
// and its objects are stored under the plural and the group.
var coreGroups = []string{api.Group, api.RBACGroup, api.APIExtensionsGroup, api.AdmissionRegistrationGroup}

func (s *service) Labels(ctx context.Context, group, resource, name string) (map[string]string, error) {
	plural := resource
	if !slices.Contains(coreGroups, group) {
		plural = resource + "." + group
	}
	if strings.Contains(name, "/") {
		return nil, nil
	}
	e, err := s.st.Get(ctx, "/registry/"+plural+"/"+name)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(e.Value, &stored); err != nil {
		return nil, fmt.Errorf("decode %s: %w", e.Key, err)
	}
	return stored.Metadata.Labels, nil
}

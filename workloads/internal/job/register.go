package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/garamsh/gaon/lib/api"
)

// register registers each kind the unit serves.
func (s *service) register(ctx context.Context) error {
	for _, want := range []*api.CustomResourceDefinition{Definition(), CronJobDefinition()} {
		if err := s.registerDefinition(ctx, want); err != nil {
			return err
		}
	}
	return nil
}

// registerDefinition creates the definition, or brings the stored one to this
// version's spec, and leaves it alone when it already matches.
func (s *service) registerDefinition(ctx context.Context, want *api.CustomResourceDefinition) error {
	have, err := s.definitions.GetCustomResourceDefinition(ctx, want.Metadata.Name)
	switch {
	case errors.Is(err, api.ErrNotFound):
		if _, err := s.definitions.PutCustomResourceDefinition(ctx, want); err != nil {
			return fmt.Errorf("register %s: %w", want.Metadata.Name, err)
		}
		s.log.Info("registered", "definition", want.Metadata.Name)
		return nil
	case err != nil:
		return fmt.Errorf("register %s: %w", want.Metadata.Name, err)
	}
	if sameSpec(have.Spec, want.Spec) {
		return nil
	}
	want.Metadata.ResourceVersion = have.Metadata.ResourceVersion
	if _, err := s.definitions.PutCustomResourceDefinition(ctx, want); err != nil {
		return fmt.Errorf("update %s: %w", want.Metadata.Name, err)
	}
	s.log.Info("updated", "definition", want.Metadata.Name)
	return nil
}

// sameSpec compares the encodings: a decoded schema carries bookkeeping that a
// built one does not.
func sameSpec(a, b api.CustomResourceDefinitionSpec) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

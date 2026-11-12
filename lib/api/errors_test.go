package api_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/lib/api"
)

func TestNewAPIError_AuthenticationReasonsHaveTheirHTTPStatus(t *testing.T) {
	tests := []struct {
		reason   api.Reason
		sentinel error
		code     int
	}{
		{api.ReasonUnauthorized, api.ErrUnauthorized, 401},
		{api.ReasonForbidden, api.ErrForbidden, 403},
	}
	for _, tc := range tests {
		t.Run(string(tc.reason), func(t *testing.T) {
			err := api.NewAPIError(tc.reason, "no")

			assert.Equal(t, tc.code, err.Code)
			assert.ErrorIs(t, err, tc.sentinel)
			assert.False(t, errors.Is(err, api.ErrInvalid), "control: another reason does not match")
		})
	}
}

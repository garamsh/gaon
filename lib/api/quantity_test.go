package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestParseQuantity(t *testing.T) {
	for _, s := range []string{"2", "500m", "4Gi", "1.5Gi", "128Mi", "10k", "1T"} {
		q, err := api.ParseQuantity(s)
		require.NoError(t, err, s)
		assert.Equal(t, api.Quantity(s), q)
	}
	for _, s := range []string{"", "Gi", "4GB", "4 Gi", "-1", "1.Gi", "4gi", "0x10"} {
		_, err := api.ParseQuantity(s)
		assert.Error(t, err, "%q", s)
	}
}

func TestQuantityDecodingRejectsMalformedForm(t *testing.T) {
	var ok api.ResourceList
	require.NoError(t, json.Unmarshal([]byte(`{"cpu":"500m","memory":"4Gi"}`), &ok))
	assert.Equal(t, api.ResourceList{CPU: "500m", Memory: "4Gi"}, ok)

	var bad api.ResourceList
	assert.Error(t, json.Unmarshal([]byte(`{"memory":"4GB"}`), &bad))
	assert.Error(t, json.Unmarshal([]byte(`{"cpu":2}`), &bad), "a number is not a quantity string")
}

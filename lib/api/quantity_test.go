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
	for _, doc := range []string{`{"cpu":1e3}`, `{"cpu":-1}`, `{"cpu":true}`, `{"cpu":null}`, `{"cpu":""}`, `{"cpu":[2]}`} {
		assert.Error(t, json.Unmarshal([]byte(doc), &bad), doc)
	}
}

func TestQuantityDecodesAJSONNumberAsItsStringForm(t *testing.T) {
	for _, s := range []string{"2", "0.5", "1.25", "0"} {
		var fromNumber, fromString api.ResourceList
		require.NoError(t, json.Unmarshal([]byte(`{"cpu":`+s+`}`), &fromNumber), s)
		require.NoError(t, json.Unmarshal([]byte(`{"cpu":"`+s+`"}`), &fromString), s)
		assert.Equal(t, api.Quantity(s), fromNumber.CPU, "the number %s keeps its literal form", s)
		assert.Equal(t, fromString, fromNumber, s)
	}
}

func TestQuantityValue(t *testing.T) {
	for q, want := range map[api.Quantity]string{
		"":      "0",
		"0":     "0",
		"2":     "2",
		"1.5":   "3/2",
		"500m":  "1/2",
		"1k":    "1000",
		"1M":    "1000000",
		"1G":    "1000000000",
		"1T":    "1000000000000",
		"1Ki":   "1024",
		"1Mi":   "1048576",
		"1Gi":   "1073741824",
		"1Ti":   "1099511627776",
		"1.5Gi": "1610612736",
	} {
		got, err := q.Value()
		require.NoError(t, err, string(q))
		assert.Equal(t, want, got.RatString(), "value of %q", string(q))
	}
	for _, q := range []api.Quantity{"4GB", "Gi", "-1", "1e3", "4 Gi"} {
		got, err := q.Value()
		assert.Error(t, err, string(q))
		assert.Nil(t, got, string(q))
	}
}

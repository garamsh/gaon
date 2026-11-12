package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestLabelSelectorSelector(t *testing.T) {
	tests := map[string]struct {
		sel    api.LabelSelector
		labels map[string]string
		valid  bool
		match  bool
	}{
		"empty matches all":          {api.LabelSelector{}, map[string]string{"a": "b"}, true, true},
		"empty matches no labels":    {api.LabelSelector{}, nil, true, true},
		"matchLabels equal":          {api.LabelSelector{MatchLabels: map[string]string{"a": "b"}}, map[string]string{"a": "b", "c": "d"}, true, true},
		"matchLabels differ":         {api.LabelSelector{MatchLabels: map[string]string{"a": "b"}}, map[string]string{"a": "x"}, true, false},
		"matchLabels all hold":       {api.LabelSelector{MatchLabels: map[string]string{"a": "b", "c": "d"}}, map[string]string{"a": "b"}, true, false},
		"In holds":                   {expr("a", api.LabelSelectorIn, "b", "c"), map[string]string{"a": "c"}, true, true},
		"In does not hold":           {expr("a", api.LabelSelectorIn, "b", "c"), map[string]string{"a": "d"}, true, false},
		"NotIn holds":                {expr("a", api.LabelSelectorNotIn, "b"), map[string]string{"a": "c"}, true, true},
		"NotIn on an absent label":   {expr("a", api.LabelSelectorNotIn, "b"), nil, true, true},
		"NotIn does not hold":        {expr("a", api.LabelSelectorNotIn, "b"), map[string]string{"a": "b"}, true, false},
		"Exists holds":               {expr("a", api.LabelSelectorExists), map[string]string{"a": ""}, true, true},
		"Exists does not hold":       {expr("a", api.LabelSelectorExists), nil, true, false},
		"DoesNotExist holds":         {expr("a", api.LabelSelectorDoesNotExist), nil, true, true},
		"DoesNotExist not hold":      {expr("a", api.LabelSelectorDoesNotExist), map[string]string{"a": "b"}, true, false},
		"unknown operator":           {expr("a", "Gt", "1"), nil, false, false},
		"In without values":          {expr("a", api.LabelSelectorIn), nil, false, false},
		"NotIn without values":       {expr("a", api.LabelSelectorNotIn), nil, false, false},
		"Exists with values":         {expr("a", api.LabelSelectorExists, "b"), nil, false, false},
		"DoesNotExist with values":   {expr("a", api.LabelSelectorDoesNotExist, "b"), nil, false, false},
		"bad key in matchLabels":     {api.LabelSelector{MatchLabels: map[string]string{"bad key!": "b"}}, nil, false, false},
		"bad value in matchLabels":   {api.LabelSelector{MatchLabels: map[string]string{"a": "bad value!"}}, nil, false, false},
		"bad key in an expression":   {expr("bad key!", api.LabelSelectorExists), nil, false, false},
		"bad value in an expression": {expr("a", api.LabelSelectorIn, "bad value!"), nil, false, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s, err := tt.sel.Selector()
			if !tt.valid {
				assert.ErrorIs(t, err, api.ErrInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.match, s.Matches(tt.labels))
		})
	}
}

func TestLabelSelectorMatchesWhenBothFormsHold(t *testing.T) {
	l := api.LabelSelector{
		MatchLabels:      map[string]string{"app": "web"},
		MatchExpressions: []api.LabelSelectorRequirement{{Key: "tier", Operator: api.LabelSelectorIn, Values: []string{"front"}}},
	}
	s, err := l.Selector()
	require.NoError(t, err)
	assert.True(t, s.Matches(map[string]string{"app": "web", "tier": "front"}))
	assert.False(t, s.Matches(map[string]string{"app": "web"}), "the expression fails")
	assert.False(t, s.Matches(map[string]string{"tier": "front"}), "the matchLabels entry fails")
}

func expr(key string, op api.LabelSelectorOp, values ...string) api.LabelSelector {
	return api.LabelSelector{MatchExpressions: []api.LabelSelectorRequirement{{Key: key, Operator: op, Values: values}}}
}

package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func mustSelector(t *testing.T, text string) api.Selector {
	t.Helper()
	s, err := api.ParseSelector(text)
	require.NoError(t, err, text)
	return s
}

func TestParseSelectorReadsEveryForm(t *testing.T) {
	for text, want := range map[string]api.Requirement{
		"app=web":                {Key: "app", Operator: api.SelectorIn, Values: []string{"web"}},
		"app==web":               {Key: "app", Operator: api.SelectorIn, Values: []string{"web"}},
		"app = web":              {Key: "app", Operator: api.SelectorIn, Values: []string{"web"}},
		"app!=web":               {Key: "app", Operator: api.SelectorNotIn, Values: []string{"web"}},
		"app in (web, db)":       {Key: "app", Operator: api.SelectorIn, Values: []string{"web", "db"}},
		"app notin (web,db)":     {Key: "app", Operator: api.SelectorNotIn, Values: []string{"web", "db"}},
		"app":                    {Key: "app", Operator: api.SelectorExists},
		"!app":                   {Key: "app", Operator: api.SelectorDoesNotExist},
		"example.com/tier=front": {Key: "example.com/tier", Operator: api.SelectorIn, Values: []string{"front"}},
		"app=":                   {Key: "app", Operator: api.SelectorIn, Values: []string{""}},
	} {
		s, err := api.ParseSelector(text)
		require.NoError(t, err, text)
		assert.Equal(t, api.Selector{want}, s, text)
	}
}

func TestParseSelectorJoinsRequirementsByComma(t *testing.T) {
	s := mustSelector(t, "app in (web,db), tier!=back ,!debug")
	assert.Equal(t, api.Selector{
		{Key: "app", Operator: api.SelectorIn, Values: []string{"web", "db"}},
		{Key: "tier", Operator: api.SelectorNotIn, Values: []string{"back"}},
		{Key: "debug", Operator: api.SelectorDoesNotExist},
	}, s, "a comma inside parentheses is a list, outside it is an AND")
}

func TestParseSelectorOfNothingSelectsEverything(t *testing.T) {
	for _, text := range []string{"", "   "} {
		s := mustSelector(t, text)
		assert.Empty(t, s)
		assert.True(t, s.Matches(map[string]string{"any": "thing"}))
		assert.True(t, s.Matches(nil))
	}
}

func TestParseSelectorRefusesBadText(t *testing.T) {
	mustSelector(t, "app=web,tier in (a,b)")

	for _, text := range []string{
		"app=web,",
		",app=web",
		"app=web,,tier=a",
		"app in ()",
		"app in",
		"app in (a",
		"app in a,b)",
		"app notin ( )",
		"app=a=b",
		"app!",
		"!",
		"=web",
		"app=-bad",
		"-bad=web",
		"app in (good,-bad)",
		"bad key=v",
		"app web",
		"app>3",
		"app ~ web",
		"!app=web",
	} {
		_, err := api.ParseSelector(text)
		assert.ErrorIs(t, err, api.ErrInvalid, "text %q", text)
	}
}

func TestSelectorMatches(t *testing.T) {
	labels := map[string]string{"app": "web", "tier": "front"}
	for text, want := range map[string]bool{
		"app=web":              true,
		"app=db":               false,
		"app!=db":              true,
		"app!=web":             false,
		"missing!=web":         true,
		"missing=web":          false,
		"app in (web,db)":      true,
		"app in (db,cache)":    false,
		"app notin (db,cache)": true,
		"app notin (web,db)":   false,
		"missing notin (a)":    true,
		"app":                  true,
		"missing":              false,
		"!missing":             true,
		"!app":                 false,
		"app=web,tier=front":   true,
		"app=web,tier=back":    false,
	} {
		assert.Equal(t, want, mustSelector(t, text).Matches(labels), text)
	}
	assert.True(t, mustSelector(t, "!app").Matches(nil), "an object without labels")
}

func TestSelectorWithinIsWhatARuleAllows(t *testing.T) {
	for _, tc := range []struct {
		asked, rule string
		within      bool
	}{
		{"app=web", "app=web", true},
		{"app=web,tier=front", "app=web", true},
		{"app=web", "", true},
		{"", "", true},
		{"", "app=web", false},
		{"tier=front", "app=web", false},
		{"app=db", "app=web", false},
		{"app!=web", "app=web", false},
		{"app", "app=web", false},
		{"app in (web)", "app in (web,db)", true},
		{"app in (web,cache)", "app in (web,db)", false},
		{"app=web", "app in (web,db)", true},
		{"app=web", "app", true},
		{"app in (web,db)", "app", true},
		{"app", "app", true},
		{"app!=web", "app", false},
		{"!app", "!app", true},
		{"app", "!app", false},
		{"app=web", "app!=db", true},
		{"app in (web,cache)", "app notin (db)", true},
		{"app in (web,db)", "app notin (db)", false},
		{"app notin (db,cache)", "app notin (db)", true},
		{"app notin (cache)", "app notin (db)", false},
		{"app!=db", "app notin (db)", true},
	} {
		assert.Equal(t, tc.within, mustSelector(t, tc.asked).Within(mustSelector(t, tc.rule)), "%q within %q", tc.asked, tc.rule)
	}
}

func TestSelectorStringParsesBackToTheSameSelection(t *testing.T) {
	for _, text := range []string{"", "app=web", "app!=web", "app in (web,db)", "app notin (web,db)", "app", "!app", "tier=front,app in (a,b),!debug"} {
		s := mustSelector(t, text)
		again := mustSelector(t, s.String())
		for _, labels := range []map[string]string{nil, {"app": "web"}, {"app": "db", "tier": "front"}, {"app": "a", "tier": "front"}, {"debug": "1"}} {
			assert.Equal(t, s.Matches(labels), again.Matches(labels), "%q as %q on %v", text, s.String(), labels)
		}
	}
	assert.Equal(t, "app=web,tier=front", mustSelector(t, "tier=front,app=web").String(), "requirements come out sorted")
}

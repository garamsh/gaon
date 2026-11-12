package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestClusterRoleValidateRefusesALabelSelectorThatDoesNotParse(t *testing.T) {
	good := newClusterRole("reader")
	good.Rules[0].LabelSelector = "app=web,tier in (a,b),!debug"
	require.NoError(t, good.Validate(), "control")

	for _, text := range []string{"app=web,", "app in ()", "bad key=v", "app=-x", "app>3"} {
		role := newClusterRole("reader")
		role.Rules[0].LabelSelector = text
		assert.ErrorIs(t, role.Validate(), api.ErrInvalid, text)
	}
}

func TestPolicyRuleAllowsLabelsOnlyWhereTheSelectorMatches(t *testing.T) {
	rule := api.PolicyRule{LabelSelector: "app=web,tier!=back"}

	assert.True(t, rule.AllowsLabels(map[string]string{"app": "web"}), "control: an object the selector selects")
	assert.True(t, rule.AllowsLabels(map[string]string{"app": "web", "tier": "front", "extra": "x"}))
	assert.False(t, rule.AllowsLabels(map[string]string{"app": "db"}), "an object outside the selector")
	assert.False(t, rule.AllowsLabels(map[string]string{"app": "web", "tier": "back"}))
	assert.False(t, rule.AllowsLabels(nil), "an object with no labels")

	unrestricted := api.PolicyRule{}
	assert.True(t, unrestricted.AllowsLabels(nil), "a rule without a selector allows any object")
	assert.True(t, unrestricted.AllowsLabels(map[string]string{"app": "db"}))

	broken := api.PolicyRule{LabelSelector: "app in ("}
	assert.False(t, broken.AllowsLabels(map[string]string{"app": "web"}), "a selector that does not parse allows nothing")
}

func TestPolicyRuleAllowsASelectorOnlyWithinItsOwn(t *testing.T) {
	rule := api.PolicyRule{LabelSelector: "app=web"}

	assert.True(t, rule.AllowsSelector(mustSelector(t, "app=web")), "control: the same selector")
	assert.True(t, rule.AllowsSelector(mustSelector(t, "app=web,tier=front")), "a narrower selector")
	assert.False(t, rule.AllowsSelector(nil), "a list with no selector asks for every object")
	assert.False(t, rule.AllowsSelector(mustSelector(t, "tier=front")), "a selector that does not mention the rule's")
	assert.False(t, rule.AllowsSelector(mustSelector(t, "app=db")))
	assert.False(t, rule.AllowsSelector(mustSelector(t, "app")), "any app, wider than app=web")

	unrestricted := api.PolicyRule{}
	assert.True(t, unrestricted.AllowsSelector(nil), "a rule without a selector allows any list")
	assert.True(t, unrestricted.AllowsSelector(mustSelector(t, "app=db")))

	broken := api.PolicyRule{LabelSelector: "app in ("}
	assert.False(t, broken.AllowsSelector(mustSelector(t, "app=web")))
}

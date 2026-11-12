package api

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// LabelSelectorParam is the query parameter of a list or a watch that carries
// a label selector.
const LabelSelectorParam = "labelSelector"

// SelectorOp is the operator of one Requirement. The equality forms of the
// selector syntax are In and NotIn with one value.
type SelectorOp string

// The operators of a Requirement.
const (
	SelectorIn           SelectorOp = "in"
	SelectorNotIn        SelectorOp = "notin"
	SelectorExists       SelectorOp = "exists"
	SelectorDoesNotExist SelectorOp = "!"
)

// Requirement is one condition on a label. Values is empty for Exists and
// DoesNotExist, and holds at least one value for In and NotIn.
type Requirement struct {
	Key      string
	Operator SelectorOp
	Values   []string
}

// Selector is a set of requirements that all hold: a comma in its text is
// an AND. The empty Selector selects every object.
type Selector []Requirement

var (
	setRequirement      = regexp.MustCompile(`^([^\s=!(),]+)\s+(in|notin)\s*\(([^()]*)\)$`)
	equalityRequirement = regexp.MustCompile(`^([^\s=!(),]+)\s*(==|=|!=)\s*([^\s=!(),]*)$`)
)

// ParseSelector parses the text of a label selector, as it is in the query
// parameter labelSelector and in a rule of a ClusterRole. The forms are
// key=value, key==value, key!=value, key in (v1,v2), key notin (v1,v2), key
// and !key, joined by commas. The empty text is the empty Selector. Anything
// else, a key or a value that is not valid in a label included, is ErrInvalid.
func ParseSelector(text string) (Selector, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var s Selector
	for _, part := range splitRequirements(text) {
		r, err := parseRequirement(strings.TrimSpace(part))
		if err != nil {
			return nil, invalid("label selector %q: %v", text, err)
		}
		s = append(s, r)
	}
	return s, nil
}

// splitRequirements splits text at the commas outside parentheses.
func splitRequirements(text string) []string {
	var parts []string
	depth, start := 0, 0
	for i, c := range text {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, text[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, text[start:])
}

func parseRequirement(part string) (Requirement, error) {
	if key, negated := strings.CutPrefix(part, "!"); negated && !strings.HasPrefix(key, "=") {
		key = strings.TrimSpace(key)
		if err := validateLabelKey(key); err != nil {
			return Requirement{}, err
		}
		return Requirement{Key: key, Operator: SelectorDoesNotExist}, nil
	}
	if m := setRequirement.FindStringSubmatch(part); m != nil {
		return setOf(m[1], SelectorOp(m[2]), m[3])
	}
	if m := equalityRequirement.FindStringSubmatch(part); m != nil {
		op := SelectorIn
		if m[2] == "!=" {
			op = SelectorNotIn
		}
		return newRequirement(m[1], op, []string{m[3]})
	}
	if err := validateLabelKey(part); err != nil {
		return Requirement{}, invalid("%q is not a requirement", part)
	}
	return Requirement{Key: part, Operator: SelectorExists}, nil
}

func setOf(key string, op SelectorOp, list string) (Requirement, error) {
	if strings.TrimSpace(list) == "" {
		return Requirement{}, invalid("%s %s has no values", key, op)
	}
	var values []string
	for _, v := range strings.Split(list, ",") {
		values = append(values, strings.TrimSpace(v))
	}
	return newRequirement(key, op, values)
}

func newRequirement(key string, op SelectorOp, values []string) (Requirement, error) {
	if err := validateLabelKey(key); err != nil {
		return Requirement{}, err
	}
	for _, v := range values {
		if err := validateLabelValue(key, v); err != nil {
			return Requirement{}, err
		}
	}
	return Requirement{Key: key, Operator: op, Values: values}, nil
}

// Matches reports whether labels satisfy every requirement. A NotIn and a
// DoesNotExist hold for an object without the key.
func (s Selector) Matches(labels map[string]string) bool {
	for _, r := range s {
		if !r.matches(labels) {
			return false
		}
	}
	return true
}

func (r Requirement) matches(labels map[string]string) bool {
	v, has := labels[r.Key]
	switch r.Operator {
	case SelectorIn:
		return has && slices.Contains(r.Values, v)
	case SelectorNotIn:
		return !has || !slices.Contains(r.Values, v)
	case SelectorExists:
		return has
	default:
		return !has
	}
}

// Within reports whether every object s selects is one rule selects, so a
// list or a watch by s asks for nothing outside rule. It looks at one
// requirement of s at a time, so it can refuse an s that is within rule only
// through two of its requirements together.
func (s Selector) Within(rule Selector) bool {
	for _, want := range rule {
		if !slices.ContainsFunc(s, func(have Requirement) bool { return have.implies(want) }) {
			return false
		}
	}
	return true
}

// implies reports whether every label set that satisfies r satisfies want.
func (r Requirement) implies(want Requirement) bool {
	if r.Key != want.Key {
		return false
	}
	switch want.Operator {
	case SelectorIn:
		return r.Operator == SelectorIn && subset(r.Values, want.Values)
	case SelectorNotIn:
		return r.Operator == SelectorNotIn && subset(want.Values, r.Values) ||
			r.Operator == SelectorIn && !slices.ContainsFunc(r.Values, func(v string) bool { return slices.Contains(want.Values, v) })
	case SelectorExists:
		return r.Operator == SelectorExists || r.Operator == SelectorIn
	default:
		return r.Operator == SelectorDoesNotExist
	}
}

func subset(part, whole []string) bool {
	for _, v := range part {
		if !slices.Contains(whole, v) {
			return false
		}
	}
	return true
}

// String returns the text of s that ParseSelector parses back to the same
// selection, its requirements in sorted order.
func (s Selector) String() string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		parts = append(parts, r.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// String returns r in the selector syntax.
func (r Requirement) String() string {
	switch r.Operator {
	case SelectorExists:
		return r.Key
	case SelectorDoesNotExist:
		return "!" + r.Key
	case SelectorIn:
		if len(r.Values) == 1 {
			return r.Key + "=" + r.Values[0]
		}
	case SelectorNotIn:
		if len(r.Values) == 1 {
			return r.Key + "!=" + r.Values[0]
		}
	}
	return r.Key + " " + string(r.Operator) + " (" + strings.Join(r.Values, ",") + ")"
}

package api

import "sort"

// LabelSelectorOp is the operator of a LabelSelectorRequirement, spelled as in
// Kubernetes.
type LabelSelectorOp string

// The operators of a LabelSelectorRequirement.
const (
	LabelSelectorIn           LabelSelectorOp = "In"
	LabelSelectorNotIn        LabelSelectorOp = "NotIn"
	LabelSelectorExists       LabelSelectorOp = "Exists"
	LabelSelectorDoesNotExist LabelSelectorOp = "DoesNotExist"
)

// LabelSelector selects objects by their labels, in the structured form of
// Kubernetes: every entry of MatchLabels and every requirement of
// MatchExpressions holds. The empty LabelSelector selects every object.
type LabelSelector struct {
	MatchLabels      map[string]string          `json:"matchLabels,omitempty"`
	MatchExpressions []LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

// LabelSelectorRequirement is one condition on a label. Values holds at least
// one value for In and NotIn, and none for Exists and DoesNotExist.
type LabelSelectorRequirement struct {
	Key      string          `json:"key"`
	Operator LabelSelectorOp `json:"operator"`
	Values   []string        `json:"values,omitempty"`
}

// Selector returns l as a Selector, or ErrInvalid for a key or a value that
// is not valid in a label, an unknown operator, or values the operator does
// not take.
func (l LabelSelector) Selector() (Selector, error) {
	keys := make([]string, 0, len(l.MatchLabels))
	for k := range l.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var s Selector
	for _, k := range keys {
		r, err := newRequirement(k, SelectorIn, []string{l.MatchLabels[k]})
		if err != nil {
			return nil, invalid("matchLabels: %v", err)
		}
		s = append(s, r)
	}
	for i, e := range l.MatchExpressions {
		r, err := e.requirement()
		if err != nil {
			return nil, invalid("matchExpressions[%d]: %v", i, err)
		}
		s = append(s, r)
	}
	return s, nil
}

func (e LabelSelectorRequirement) requirement() (Requirement, error) {
	var op SelectorOp
	hasValues := false
	switch e.Operator {
	case LabelSelectorIn:
		op, hasValues = SelectorIn, true
	case LabelSelectorNotIn:
		op, hasValues = SelectorNotIn, true
	case LabelSelectorExists:
		op = SelectorExists
	case LabelSelectorDoesNotExist:
		op = SelectorDoesNotExist
	default:
		return Requirement{}, invalid("operator is %q, want In, NotIn, Exists or DoesNotExist", e.Operator)
	}
	if hasValues != (len(e.Values) > 0) {
		return Requirement{}, invalid("operator %s takes values: %t, and has %d", e.Operator, hasValues, len(e.Values))
	}
	return newRequirement(e.Key, op, e.Values)
}

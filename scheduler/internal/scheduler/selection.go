package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// refusal names who refused a Node and why.
type refusal struct{ by, reason string }

// refusals counts the Nodes each refusal kept a Machine from.
type refusals map[refusal]int

func (r refusals) String() string {
	keys := make([]refusal, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b refusal) int {
		return strings.Compare(a.by+"\x00"+a.reason, b.by+"\x00"+b.reason)
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s refused %d (%s)", k.by, r[k], k.reason)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// choose returns the Node m goes to, the empty string when no Node passes
// every filter, and the Nodes each filter or extender refused. A non-nil error
// is a failed call to an extender that is not ignorable: m is not placed this
// pass.
func (s *service) choose(ctx context.Context, book *ledger, m *api.Machine, d demand) (string, refusals, error) {
	refused := refusals{}
	var candidates []string
	for _, name := range book.names {
		view := book.views[name]
		var by, reason string
		for _, f := range filterPlugins {
			if reason = f.Filter(d, view); reason != "" {
				by = f.Name()
				break
			}
		}
		if by != "" {
			refused[refusal{by, reason}]++
			continue
		}
		candidates = append(candidates, name)
	}

	for _, e := range s.extenders {
		if len(candidates) == 0 {
			break
		}
		if e.filterVerb == "" {
			continue
		}
		passed, failed, err := e.filter(ctx, m, candidates)
		if err != nil {
			if e.ignorable {
				s.log.Warn("extender skipped", "machine", m.Metadata.Name, "err", err)
				continue
			}
			return "", refused, err
		}
		for _, reason := range failed {
			refused[refusal{e.name(), reason}]++
		}
		candidates = passed
	}
	if len(candidates) == 0 {
		return "", refused, nil
	}

	scores := map[string]int{}
	for _, ws := range s.scores {
		for _, name := range candidates {
			scores[name] += ws.weight * ws.plugin.Score(d.req, book.views[name])
		}
	}
	for _, e := range s.extenders {
		if e.prioritizeVerb == "" {
			continue
		}
		prio, err := e.prioritize(ctx, m, candidates)
		if err != nil {
			if e.ignorable {
				s.log.Warn("extender skipped", "machine", m.Metadata.Name, "err", err)
				continue
			}
			return "", refused, err
		}
		for name, score := range prio {
			scores[name] += score * e.weight * (maxScore / maxExtenderScore)
		}
	}

	best := candidates[0]
	for _, name := range candidates[1:] {
		if scores[name] > scores[best] {
			best = name
		}
	}
	return best, refused, nil
}

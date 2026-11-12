package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

const (
	maxExtenderScore = 10
	maxExtenderBody  = 8 << 20
)

// extenderArgs is the body of a filter and of a prioritize request: the
// MicroVM and the names of the Nodes still in the running.
type extenderArgs struct {
	MicroVM   *api.MicroVM `json:"microvm"`
	NodeNames []string     `json:"nodeNames"`
}

// extenderFilterResult is the answer to a filter request. A Node sent and in
// neither NodeNames nor FailedNodes is refused with no reason of the
// extender's.
type extenderFilterResult struct {
	NodeNames   []string          `json:"nodeNames"`
	FailedNodes map[string]string `json:"failedNodes"`
	Error       string            `json:"error"`
}

// extenderPriority is one entry of the answer to a prioritize request; a Node
// the answer omits scores 0.
type extenderPriority struct {
	Host  string `json:"host"`
	Score int    `json:"score"`
}

// extender is an out-of-process filter and score source the Scheduler calls
// over HTTP. It never binds.
type extender struct {
	urlPrefix      string
	filterVerb     string
	prioritizeVerb string
	weight         int
	ignorable      bool
	client         *http.Client
}

func (e *extender) name() string { return "extender " + e.urlPrefix }

// filter returns the names of nodeNames the extender passes, in the order
// given, and the reason for each one it refuses.
func (e *extender) filter(ctx context.Context, m *api.MicroVM, nodeNames []string) ([]string, map[string]string, error) {
	var res extenderFilterResult
	if err := e.call(ctx, e.filterVerb, m, nodeNames, &res); err != nil {
		return nil, nil, err
	}
	if res.Error != "" {
		return nil, nil, fmt.Errorf("%s %s: %s", e.name(), e.filterVerb, res.Error)
	}
	for _, n := range res.NodeNames {
		if !slices.Contains(nodeNames, n) {
			return nil, nil, fmt.Errorf("%s %s: passes Node %q, which was not sent", e.name(), e.filterVerb, n)
		}
	}
	passed := slices.DeleteFunc(slices.Clone(nodeNames), func(n string) bool { return !slices.Contains(res.NodeNames, n) })
	failed := map[string]string{}
	for _, n := range nodeNames {
		if slices.Contains(passed, n) {
			continue
		}
		reason := res.FailedNodes[n]
		if reason == "" {
			reason = "not passed"
		}
		failed[n] = reason
	}
	return passed, failed, nil
}

// prioritize returns the extender's score, 0 to maxExtenderScore, for each of
// nodeNames it names.
func (e *extender) prioritize(ctx context.Context, m *api.MicroVM, nodeNames []string) (map[string]int, error) {
	var res []extenderPriority
	if err := e.call(ctx, e.prioritizeVerb, m, nodeNames, &res); err != nil {
		return nil, err
	}
	scores := map[string]int{}
	for _, p := range res {
		if !slices.Contains(nodeNames, p.Host) {
			return nil, fmt.Errorf("%s %s: scores Node %q, which was not sent", e.name(), e.prioritizeVerb, p.Host)
		}
		if p.Score < 0 || p.Score > maxExtenderScore {
			return nil, fmt.Errorf("%s %s: score %d for Node %q is outside 0 to %d", e.name(), e.prioritizeVerb, p.Score, p.Host, maxExtenderScore)
		}
		scores[p.Host] = p.Score
	}
	return scores, nil
}

// call POSTs the MicroVM and the Node names to the verb and decodes a 200
// answer into out.
func (e *extender) call(ctx context.Context, verb string, m *api.MicroVM, nodeNames []string, out any) error {
	body, err := json.Marshal(extenderArgs{MicroVM: m, NodeNames: nodeNames})
	if err != nil {
		return fmt.Errorf("%s %s: %w", e.name(), verb, err)
	}
	url := strings.TrimRight(e.urlPrefix, "/") + "/" + verb
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s %s: %w", e.name(), verb, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", e.name(), verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d", e.name(), verb, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxExtenderBody)).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode answer: %w", e.name(), verb, err)
	}
	return nil
}

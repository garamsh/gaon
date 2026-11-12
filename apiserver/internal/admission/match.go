// Package admission calls the admission webhooks of ADR 0010 for a write: it
// selects the webhooks whose rules match, sends each an AdmissionReview and
// applies the patches and the refusals they answer.
package admission

import (
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// matches reports whether any rule of w selects the write.
func matches(w api.Webhook, resource api.GroupVersionResource, sub string, op api.Operation) bool {
	return slices.ContainsFunc(w.Rules, func(r api.WebhookRule) bool {
		return (slices.Contains(r.Operations, api.OperationAll) || slices.Contains(r.Operations, op)) &&
			anyOf(r.APIGroups, resource.Group) &&
			anyOf(r.APIVersions, resource.Version) &&
			slices.ContainsFunc(r.Resources, func(pattern string) bool { return matchesResource(pattern, resource.Resource, sub) })
	})
}

func anyOf(patterns []string, value string) bool {
	return slices.Contains(patterns, "*") || slices.Contains(patterns, value)
}

// matchesResource applies one entry of a rule's resources: "*" is every
// resource and no subresource, "*/*" every resource and subresource, and
// "resource/subresource" takes "*" for either part, and a "*" subresource
// matches the resource itself as well as each of its subresources.
func matchesResource(pattern, resource, sub string) bool {
	if pattern == "*" {
		return sub == ""
	}
	res, subPattern, hasSub := strings.Cut(pattern, "/")
	if res != "*" && res != resource {
		return false
	}
	if !hasSub {
		return sub == ""
	}
	return subPattern == "*" || subPattern == sub
}

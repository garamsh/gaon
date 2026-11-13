package fakegaon

import (
	"net/http"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// SetNetworkPolicy stores p under a new version, as a user does. The fake
// serves the list of NetworkPolicies, which the Node authorizer allows a Node,
// and no watch.
func (s *Server) SetNetworkPolicy(p api.NetworkPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNetworkPolicy}
	s.rev++
	p.Metadata.ResourceVersion = version(s.rev)
	s.policies[p.Metadata.Name] = p
}

// DeleteNetworkPolicy removes the NetworkPolicy name.
func (s *Server) DeleteNetworkPolicy(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.policies, name)
	s.rev++
}

func (s *Server) listNetworkPolicies(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := api.NetworkPolicyList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNetworkPolicyList},
		Metadata: api.ListMeta{ResourceVersion: version(s.rev)},
		Items:    []api.NetworkPolicy{},
	}
	for _, p := range s.policies {
		list.Items = append(list.Items, p)
	}
	slices.SortFunc(list.Items, func(a, b api.NetworkPolicy) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	writeJSON(w, http.StatusOK, list)
}

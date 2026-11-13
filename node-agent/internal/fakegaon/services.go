package fakegaon

import (
	"net/http"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// SetService stores svc under a new version, as the API server does. The fake
// serves the lists of Services and Endpoints, which the Node authorizer allows
// a Node, and no watch.
func (s *Server) SetService(svc api.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService}
	s.rev++
	svc.Metadata.ResourceVersion = version(s.rev)
	s.services[svc.Metadata.Name] = svc
}

// DeleteService removes the Service name.
func (s *Server) DeleteService(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.services, name)
	s.rev++
}

// SetEndpoints stores e under a new version, as the Endpoints controller does.
func (s *Server) SetEndpoints(e api.Endpoints) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints}
	s.rev++
	e.Metadata.ResourceVersion = version(s.rev)
	s.endpoints[e.Metadata.Name] = e
}

// DeleteEndpoints removes the Endpoints name.
func (s *Server) DeleteEndpoints(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.endpoints, name)
	s.rev++
}

func (s *Server) listServices(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := api.ServiceList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceList},
		Metadata: api.ListMeta{ResourceVersion: version(s.rev)},
		Items:    []api.Service{},
	}
	for _, svc := range s.services {
		list.Items = append(list.Items, svc)
	}
	slices.SortFunc(list.Items, func(a, b api.Service) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) listEndpoints(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := api.EndpointsList{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpointsList},
		Metadata: api.ListMeta{ResourceVersion: version(s.rev)},
		Items:    []api.Endpoints{},
	}
	for _, e := range s.endpoints {
		list.Items = append(list.Items, e)
	}
	slices.SortFunc(list.Items, func(a, b api.Endpoints) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	writeJSON(w, http.StatusOK, list)
}

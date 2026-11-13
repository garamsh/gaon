// Package zone holds the Services and Endpoints the DNS unit has read and
// answers the questions under its zone from them.
package zone

import (
	"fmt"
	"sync"

	"github.com/miekg/dns"

	"github.com/garamsh/gaon/lib/api"
)

// Namespace is the label every Service name carries: Services have no
// namespace yet, so it is a constant.
const Namespace = "default"

// TTL is the lifetime in seconds of every answer and of the start of authority
// of a negative one.
const TTL = 5

// Zone is the set of names `<service>.default.svc.<zone>` that the Services it
// holds make. It is safe for concurrent use.
type Zone struct {
	apex string

	mu              sync.RWMutex
	services        map[string]api.Service
	endpoints       map[string]api.Endpoints
	servicesListed  bool
	endpointsListed bool
}

// New returns an empty Zone for the cluster domain name, such as
// cluster.local. A name that is not a domain name, or is the root, is an error.
func New(name string) (*Zone, error) {
	if _, ok := dns.IsDomainName(name); !ok || dns.CanonicalName(name) == "." {
		return nil, fmt.Errorf("zone %q is not a domain name below the root", name)
	}
	return &Zone{
		apex:      dns.CanonicalName(name),
		services:  map[string]api.Service{},
		endpoints: map[string]api.Endpoints{},
	}, nil
}

// Covers reports whether qname is the zone or below it, the names Answer
// settles and that are never forwarded.
func (z *Zone) Covers(qname string) bool {
	return dns.IsSubDomain(z.apex, dns.CanonicalName(qname))
}

// ReplaceServices makes items the Services of the zone and marks them listed.
func (z *Zone) ReplaceServices(items []api.Service) {
	next := make(map[string]api.Service, len(items))
	for _, s := range items {
		next[s.Metadata.Name] = s
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	z.services, z.servicesListed = next, true
}

// ApplyService adds or changes s for an Added or Modified event and removes it
// for a Deleted one.
func (z *Zone) ApplyService(t api.EventType, s api.Service) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if t == api.EventDeleted {
		delete(z.services, s.Metadata.Name)
		return
	}
	z.services[s.Metadata.Name] = s
}

// ReplaceEndpoints makes items the Endpoints of the zone and marks them listed.
func (z *Zone) ReplaceEndpoints(items []api.Endpoints) {
	next := make(map[string]api.Endpoints, len(items))
	for _, e := range items {
		next[e.Metadata.Name] = e
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	z.endpoints, z.endpointsListed = next, true
}

// ApplyEndpoints adds or changes e for an Added or Modified event and removes
// it for a Deleted one.
func (z *Zone) ApplyEndpoints(t api.EventType, e api.Endpoints) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if t == api.EventDeleted {
		delete(z.endpoints, e.Metadata.Name)
		return
	}
	z.endpoints[e.Metadata.Name] = e
}

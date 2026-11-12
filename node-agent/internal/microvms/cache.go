package microvms

import (
	"sort"

	"github.com/garamsh/gaon/lib/api"
)

// cache is the agent's view of the MicroVMs, owned by the one goroutine that
// reconciles. It never lets a MicroVM's epoch go back: a binding older than
// the newest epoch seen is ignored, whatever delivered it.
type cache struct {
	node     string
	microvms map[string]api.MicroVM
	newest   map[string]api.Epoch
}

func newCache(node string) *cache {
	return &cache{node: node, microvms: map[string]api.MicroVM{}, newest: map[string]api.Epoch{}}
}

// observe records m and reports whether it was taken: it is not when its
// epoch is below the newest seen for its name.
func (c *cache) observe(m api.MicroVM) bool {
	name := m.Metadata.Name
	if m.Spec.BindingEpoch < c.newest[name] {
		return false
	}
	c.newest[name] = m.Spec.BindingEpoch
	c.microvms[name] = m
	return true
}

// forget removes a deleted MicroVM, unless the event is older than the
// newest binding seen.
func (c *cache) forget(m api.MicroVM) bool {
	name := m.Metadata.Name
	if m.Spec.BindingEpoch < c.newest[name] {
		return false
	}
	delete(c.microvms, name)
	delete(c.newest, name)
	return true
}

// replace rebuilds the cache from a list: a MicroVM the list lacks is gone.
func (c *cache) replace(list []api.MicroVM) {
	listed := map[string]bool{}
	for _, m := range list {
		listed[m.Metadata.Name] = true
		c.observe(m)
	}
	for name := range c.microvms {
		if !listed[name] {
			delete(c.microvms, name)
			delete(c.newest, name)
		}
	}
}

// mine reports whether m is to run here now: bound to this node and not
// being deleted.
func (c *cache) mine(m api.MicroVM) bool {
	return m.Spec.NodeName == c.node && m.Metadata.DeletionTimestamp == nil
}

// wanted returns the mvmd identifiers of the MicroVMs to run here.
func (c *cache) wanted() map[string]bool {
	ids := map[string]bool{}
	for _, m := range c.microvms {
		if c.mine(m) {
			ids[machineID(m.Metadata.Name, m.Spec.BindingEpoch)] = true
		}
	}
	return ids
}

// names returns the cached MicroVMs' names in order.
func (c *cache) names() []string {
	names := make([]string, 0, len(c.microvms))
	for name := range c.microvms {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

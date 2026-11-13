package resourceapi

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net/netip"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// Ranges are the two ranges Service addresses are allocated from: the service
// prefix, which is virtual, and the LAN pool, which is real LAN addresses. A
// zero prefix is a range the server was not given.
type Ranges struct {
	ClusterIP    netip.Prefix
	LoadBalancer netip.Prefix
}

// The two address spaces, which are the second segment of an allocation key.
const (
	spaceClusterIP = "service-ip"
	spaceLAN       = "lan"
)

func allocationPrefix(space string) string { return "/allocations/" + space + "/" }

// claimKey is the key that records that a Service holds addr of space.
func claimKey(space string, addr netip.Addr) string { return allocationPrefix(space) + addr.String() }

// allocate chooses the addresses the new Service o holds, writes them into o,
// and returns the writes that record them, each requiring its key to be absent.
// An address o asks for is checked against its range and against the keys held
// now; one it does not ask for is chosen from the free addresses of its range.
// Nothing is stored here: the writes go with the create of o, in one step.
func (s *service) allocate(ctx context.Context, o *api.Service) ([]Write, error) {
	var holds []Write
	o.Status = api.ServiceStatus{}
	name := o.Metadata.Name
	if !o.Spec.Headless() {
		addr, err := s.choose(ctx, spaceClusterIP, s.ranges.ClusterIP, "spec.clusterIP", "service-cluster-ip-range", o.Spec.ClusterIP)
		if err != nil {
			return nil, err
		}
		o.Spec.ClusterIP = firstNonEmpty(o.Spec.ClusterIP, addr.String())
		holds = append(holds, Write{Key: claimKey(spaceClusterIP, addr), Value: []byte(name)})
	}
	if o.Spec.EffectiveType() == api.ServiceLoadBalancer {
		addr, err := s.choose(ctx, spaceLAN, s.ranges.LoadBalancer, "spec.loadBalancerIP", "load-balancer-pool", o.Spec.LoadBalancerIP)
		if err != nil {
			return nil, err
		}
		o.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: addr.String()}}}
		holds = append(holds, Write{Key: claimKey(spaceLAN, addr), Value: []byte(name)})
	}
	return holds, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// choose is the address of rng that a Service asks for, or where it asks for
// none one that is free. flag names the range in an error.
func (s *service) choose(ctx context.Context, space string, rng netip.Prefix, field, flag, requested string) (netip.Addr, error) {
	if !rng.IsValid() {
		return netip.Addr{}, internalError(errors.New("the API server was started without --" + flag + ", so it has no addresses to allocate for " + field))
	}
	entries, _, err := s.st.List(ctx, allocationPrefix(space))
	if err != nil {
		return netip.Addr{}, err
	}
	held := make(map[netip.Addr]bool, len(entries))
	for _, e := range entries {
		if a, err := netip.ParseAddr(strings.TrimPrefix(e.Key, allocationPrefix(space))); err == nil {
			held[a] = true
		}
	}
	if requested != "" {
		addr, err := netip.ParseAddr(requested)
		if err != nil || !usable(rng, addr) {
			return netip.Addr{}, api.NewAPIError(api.ReasonInvalid, "%s is %q, want an address of %s, the --%s range, that is not its network or broadcast address", field, requested, rng, flag)
		}
		if held[addr] {
			return netip.Addr{}, api.NewAPIError(api.ReasonConflict, "%s %q is held by another Service", field, requested)
		}
		return addr, nil
	}
	addr, ok, err := pick(rng, held)
	if err != nil {
		return netip.Addr{}, err
	}
	if !ok {
		return netip.Addr{}, api.NewAPIError(api.ReasonConflict, "every address of %s, the --%s range, is held", rng, flag)
	}
	return addr, nil
}

// bounds is the offsets from the start of rng of the addresses a Service may
// hold: all of them, except for an IPv4 range of four or more addresses, whose
// first (the network) and last (the broadcast) are left out.
func bounds(rng netip.Prefix) (lo, hi *big.Int) {
	bits := rng.Addr().BitLen() - rng.Bits()
	hi = new(big.Int).Lsh(big.NewInt(1), uint(bits))
	lo = new(big.Int)
	if rng.Addr().Is4() && bits >= 2 {
		lo.SetInt64(1)
		hi.Sub(hi, big.NewInt(1))
	}
	return lo, hi
}

func asInt(a netip.Addr) *big.Int { return new(big.Int).SetBytes(a.AsSlice()) }

func fromInt(i *big.Int, is4 bool) netip.Addr {
	size := 16
	if is4 {
		size = 4
	}
	b := make([]byte, size)
	i.FillBytes(b)
	a, _ := netip.AddrFromSlice(b)
	return a
}

// usable reports whether addr is an address of rng a Service may hold.
func usable(rng netip.Prefix, addr netip.Addr) bool {
	if !rng.Contains(addr) {
		return false
	}
	lo, hi := bounds(rng)
	off := new(big.Int).Sub(asInt(addr), asInt(rng.Masked().Addr()))
	return off.Cmp(lo) >= 0 && off.Cmp(hi) < 0
}

// pick returns a usable address of rng that is not in held, starting at a
// random one and going up, wrapping at the end. Among held+1 addresses in a
// row at least one is free, so the scan is bounded by what is held and not by
// the size of rng. ok is false where every address is held.
func pick(rng netip.Prefix, held map[netip.Addr]bool) (addr netip.Addr, ok bool, err error) {
	lo, hi := bounds(rng)
	n := new(big.Int).Sub(hi, lo)
	start, err := rand.Int(rand.Reader, n)
	if err != nil {
		return netip.Addr{}, false, err
	}
	steps := new(big.Int).Add(big.NewInt(int64(len(held))), big.NewInt(1))
	if n.Cmp(steps) < 0 {
		steps = n
	}
	base := asInt(rng.Masked().Addr())
	for i := new(big.Int); i.Cmp(steps) < 0; i.Add(i, big.NewInt(1)) {
		off := new(big.Int).Add(start, i)
		off.Mod(off, n).Add(off, lo)
		a := fromInt(off.Add(off, base), rng.Addr().Is4())
		if !held[a] {
			return a, true, nil
		}
	}
	return netip.Addr{}, false, nil
}

// createService stores the new Service o with the keys of its addresses in one
// step. A key that another API server took between the read that chose the
// address and the write fails the step, and the addresses are chosen again, at
// most fenceAttempts times, then the create is a Conflict.
func (s *service) createService(ctx context.Context, o *api.Service) (*api.Service, error) {
	if _, err := s.st.Get(ctx, services.key(o.Metadata.Name)); err == nil {
		return nil, services.translate(ErrExists, o.Metadata.Name)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	asked := *o
	for range fenceAttempts {
		*o = asked
		holds, err := s.allocate(ctx, o)
		if err != nil {
			return nil, err
		}
		fenced := *s
		fenced.st = fencedStore{Store: s.st, fence: holds}
		created, err := admittedPut(ctx, &fenced, services, o, func() error { return nil }, func(*api.Service) error { return nil })
		if !errors.Is(err, errFenceMoved) {
			return created, err
		}
	}
	return nil, api.NewAPIError(api.ReasonConflict, "services %q could not be given its addresses after %d attempts: another API server took them first; retry", asked.Metadata.Name, fenceAttempts)
}

// DeleteService removes the Service if rv is its current version, and in the
// same step the keys of the addresses it holds.
func (s *service) DeleteService(ctx context.Context, name string, rv api.ResourceVersion) (*api.Service, error) {
	o, rev, err := services.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(services.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, services, o); err != nil {
		return nil, err
	}
	writes := []Write{{Key: services.key(name), Revision: rev, Delete: true}}
	held := map[string]string{}
	if !o.Spec.Headless() && o.Spec.ClusterIP != "" {
		held[allocationPrefix(spaceClusterIP)+o.Spec.ClusterIP] = name
	}
	if lb := o.Status.LoadBalancer; lb != nil && len(lb.Ingress) > 0 {
		held[allocationPrefix(spaceLAN)+lb.Ingress[0].IP] = name
	}
	for key, holder := range held {
		e, err := s.st.Get(ctx, canonicalKey(key))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if string(e.Value) == holder {
			writes = append(writes, Write{Key: e.Key, Revision: e.Revision, Delete: true})
		}
	}
	deleted, err := s.st.Transact(ctx, writes)
	if err != nil {
		return nil, services.translate(err, name)
	}
	o.Metadata.ResourceVersion = version(deleted)
	return o, nil
}

// canonicalKey is key with the address at its end in the form the Store holds
// it under, for an address a user wrote in another form.
func canonicalKey(key string) string {
	i := strings.LastIndex(key, "/") + 1
	if a, err := netip.ParseAddr(key[i:]); err == nil {
		return key[:i] + a.String()
	}
	return key
}

// PutService creates o, allocating its addresses, or replaces the stored
// Service o was read at, which keeps its addresses and its status.
func (s *service) PutService(ctx context.Context, name string, o *api.Service) (*api.Service, error) {
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, err
	}
	update := o.Metadata.ResourceVersion != ""
	if update {
		old, _, err := services.get(ctx, s.st, name)
		if err != nil {
			return nil, err
		}
		o.KeepAllocated(old)
	}
	if err := mutate(ctx, s, services, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, err
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if !update {
		return s.createService(ctx, o)
	}
	return admittedPut(ctx, s, services, o, func() error { return nil }, func(old *api.Service) error {
		if err := o.ValidateUpdate(old); err != nil {
			return err
		}
		o.Status = old.Status
		return nil
	})
}

func (s *service) GetService(ctx context.Context, name string) (*api.Service, error) {
	o, _, err := services.get(ctx, s.st, name)
	return o, err
}

func (s *service) ListServices(ctx context.Context, sel api.Selector) (*api.ServiceList, error) {
	items, rv, err := services.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.ServiceList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchServices(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return services.watch(ctx, s.st, rv, sel)
}

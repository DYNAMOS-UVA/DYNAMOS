package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Store is the in-memory hot path + etcd write-through for negotiations.
// Own etcd key namespace (/dsp/negotiations/{id}), one key per negotiation -
// no shared blob, so no read-modify-write clobber hazard like
// /policyEnforcer/agreements/{party} has. `id` (the ProviderPid) already
// embeds the party (see negotiation.go's newNegotiation), so the key itself
// doesn't need a separate party segment.
//
// #80: a Consumer-role negotiation keys on ConsumerPid instead (see
// Negotiation.OwnPid), under its own /dsp/negotiations/consumer/ prefix -
// a real namespace split, not just distinct-by-convention IDs, so a
// Provider-role and a Consumer-role negotiation can never collide on lookup
// even if their generated Pids ever coincided.
type Store struct {
	mu         sync.RWMutex
	cache      map[string]Negotiation
	etcdClient *clientv3.Client
}

func NewStore(etcdClient *clientv3.Client) *Store {
	return &Store{
		cache:      make(map[string]Negotiation),
		etcdClient: etcdClient,
	}
}

// negotiationKey doubles as the etcd key and the in-memory cache key - using
// the same kind-prefixed string for both means the collision guarantee is
// structural, not just "IDs happen not to collide".
func negotiationKey(kind Kind, id string) string {
	if kind == KindConsumer {
		return fmt.Sprintf("/dsp/negotiations/consumer/%s", id)
	}
	return fmt.Sprintf("/dsp/negotiations/%s", id)
}

// clone deep-copies n, including the Offer/Agreement byte slices - the cache
// and every Get caller must never share a mutable *Negotiation, or one
// request's in-place state transition can corrupt another's read.
func (n Negotiation) clone() *Negotiation {
	c := n
	if n.Offer != nil {
		c.Offer = append(json.RawMessage(nil), n.Offer...)
	}
	if n.Agreement != nil {
		c.Agreement = append(json.RawMessage(nil), n.Agreement...)
	}
	return &c
}

// Get reads the hot path first, falling back to etcd on a cache miss (e.g.
// after a restart) and populating the cache on success. Always returns a
// fresh clone - never a pointer aliasing the cache's own copy. kind is
// required (not read from the not-yet-fetched Negotiation) so callers must
// say up front which namespace they mean - see negotiationKey.
func (s *Store) Get(kind Kind, id string) (*Negotiation, error) {
	key := negotiationKey(kind, id)

	s.mu.RLock()
	if n, ok := s.cache[key]; ok {
		s.mu.RUnlock()
		return n.clone(), nil
	}
	s.mu.RUnlock()

	var n Negotiation
	raw, err := etcd.GetAndUnmarshalJSON(s.etcdClient, key, &n)
	if err != nil {
		return nil, fmt.Errorf("fetching negotiation %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %q", ErrNegotiationNotFound, id)
	}

	s.mu.Lock()
	s.cache[key] = n
	s.mu.Unlock()

	return n.clone(), nil
}

// Save write-throughs to etcd first (source of truth), then updates the hot
// path only on success - never cache a write that didn't durably land.
// Marshals and calls PutValueToEtcd directly rather than
// etcd.SaveStructToEtcd: that helper zap.Fatalw's (process-exits) on a
// marshal/put failure, which would crash this service on a transient etcd
// error instead of letting the handler return a 500.
func (s *Store) Save(n *Negotiation) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshaling negotiation %q: %w", n.OwnPid(), err)
	}

	key := negotiationKey(n.Kind, n.OwnPid())
	if err := etcd.PutValueToEtcd(s.etcdClient, key, string(payload)); err != nil {
		return fmt.Errorf("saving negotiation %q: %w", n.OwnPid(), err)
	}

	s.mu.Lock()
	s.cache[key] = *n.clone()
	s.mu.Unlock()

	return nil
}

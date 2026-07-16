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

func negotiationKey(id string) string {
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
// fresh clone - never a pointer aliasing the cache's own copy.
func (s *Store) Get(id string) (*Negotiation, error) {
	s.mu.RLock()
	if n, ok := s.cache[id]; ok {
		s.mu.RUnlock()
		return n.clone(), nil
	}
	s.mu.RUnlock()

	var n Negotiation
	raw, err := etcd.GetAndUnmarshalJSON(s.etcdClient, negotiationKey(id), &n)
	if err != nil {
		return nil, fmt.Errorf("fetching negotiation %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %q", ErrNegotiationNotFound, id)
	}

	s.mu.Lock()
	s.cache[id] = n
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
		return fmt.Errorf("marshaling negotiation %q: %w", n.ProviderPid, err)
	}

	if err := etcd.PutValueToEtcd(s.etcdClient, negotiationKey(n.ProviderPid), string(payload)); err != nil {
		return fmt.Errorf("saving negotiation %q: %w", n.ProviderPid, err)
	}

	s.mu.Lock()
	s.cache[n.ProviderPid] = *n.clone()
	s.mu.Unlock()

	return nil
}

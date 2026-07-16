package main

import (
	"fmt"
	"sync"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Store is the in-memory hot path + etcd write-through for negotiations.
// Own etcd key namespace (/dsp/negotiations/{party}/{id}), one key per
// negotiation - no shared blob, so no read-modify-write clobber hazard like
// /policyEnforcer/agreements/{party} has.
type Store struct {
	mu         sync.RWMutex
	cache      map[string]*Negotiation
	etcdClient *clientv3.Client
	party      string
}

func NewStore(etcdClient *clientv3.Client, party string) *Store {
	return &Store{
		cache:      make(map[string]*Negotiation),
		etcdClient: etcdClient,
		party:      party,
	}
}

func negotiationKey(party, id string) string {
	return fmt.Sprintf("/dsp/negotiations/%s/%s", party, id)
}

// Get reads the hot path first, falling back to etcd on a cache miss (e.g.
// after a restart) and populating the cache on success.
func (s *Store) Get(id string) (*Negotiation, error) {
	s.mu.RLock()
	if n, ok := s.cache[id]; ok {
		s.mu.RUnlock()
		return n, nil
	}
	s.mu.RUnlock()

	var n Negotiation
	raw, err := etcd.GetAndUnmarshalJSON(s.etcdClient, negotiationKey(s.party, id), &n)
	if err != nil {
		return nil, fmt.Errorf("fetching negotiation %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %q", ErrNegotiationNotFound, id)
	}

	s.mu.Lock()
	s.cache[id] = &n
	s.mu.Unlock()

	return &n, nil
}

// Save write-throughs to etcd first (source of truth), then updates the hot
// path only on success - never cache a write that didn't durably land.
func (s *Store) Save(n *Negotiation) error {
	if err := etcd.SaveStructToEtcd(s.etcdClient, negotiationKey(s.party, n.ProviderPid), n); err != nil {
		return fmt.Errorf("saving negotiation %q: %w", n.ProviderPid, err)
	}

	s.mu.Lock()
	s.cache[n.ProviderPid] = n
	s.mu.Unlock()

	return nil
}

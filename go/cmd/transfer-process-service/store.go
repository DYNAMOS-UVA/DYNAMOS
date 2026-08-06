package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Store holds the in-memory hot path and the etcd write-through for
// transfers. Each transfer gets its own etcd key, under
// /dsp/transfers/{id}. No key holds a shared blob, so there is no
// read-modify-write clobber risk like /policyEnforcer/agreements/{party}
// has. The id (the ProviderPid) already embeds the party - see
// newTransferProcess in transfer.go. The key does not need a separate
// party segment.
type Store struct {
	mu         sync.RWMutex
	cache      map[string]TransferProcess
	// consumerCache is a separate map from cache, not just a separate key
	// prefix in the same map. A Provider-role ProviderPid and a
	// Consumer-role ConsumerPid are generated under different urn
	// segments ("urn:dynamos:transfer:{party}:..." vs
	// "urn:dynamos:transfer:consumer:{party}:...") so they cannot collide
	// today, but a separate map makes that a structural guarantee rather
	// than a naming convention - mirrors negotiation-service's own
	// kind-prefixed key split (see negotiationKey), applied here as a
	// separate map instead of a prefixed key in one map, so Get/Save's
	// existing behavior and every existing caller/test stays untouched.
	consumerCache map[string]TransferProcess
	etcdClient    *clientv3.Client
}

func NewStore(etcdClient *clientv3.Client) *Store {
	return &Store{
		cache:         make(map[string]TransferProcess),
		consumerCache: make(map[string]TransferProcess),
		etcdClient:    etcdClient,
	}
}

func transferKey(id string) string {
	return fmt.Sprintf("/dsp/transfers/%s", id)
}

// consumerTransferKey is the etcd key namespace for a Consumer-role
// transfer, keyed by ConsumerPid - DYNAMOS owns that Pid when it
// initiates as Consumer. A real namespace split from transferKey, not
// just distinct-by-convention IDs, mirrors negotiation-service's own
// /dsp/negotiations/consumer/ split.
func consumerTransferKey(id string) string {
	return fmt.Sprintf("/dsp/transfers/consumer/%s", id)
}

// Get reads the hot path first. On a cache miss, for example after a
// restart, it falls back to etcd and fills the cache on success. Get
// always returns a fresh clone. It never returns a pointer into the
// cache's own copy.
func (s *Store) Get(id string) (*TransferProcess, error) {
	s.mu.RLock()
	if t, ok := s.cache[id]; ok {
		s.mu.RUnlock()
		return t.clone(), nil
	}
	s.mu.RUnlock()

	var t TransferProcess
	raw, err := etcd.GetAndUnmarshalJSON(s.etcdClient, transferKey(id), &t)
	if err != nil {
		return nil, fmt.Errorf("fetching transfer %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %q", ErrTransferNotFound, id)
	}

	s.mu.Lock()
	s.cache[id] = t
	s.mu.Unlock()

	return t.clone(), nil
}

// Save writes to etcd first - etcd is the source of truth. Save updates
// the hot path only after the etcd write succeeds. It never caches a
// write that did not land durably. Save marshals the value and calls
// PutValueToEtcd directly. It does not call etcd.SaveStructToEtcd: that
// helper calls zap.Fatalw on a marshal or put failure, which exits the
// process. A transient etcd error would then crash this service, instead
// of letting the handler return a 500.
func (s *Store) Save(t *TransferProcess) error {
	payload, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshaling transfer %q: %w", t.ProviderPid, err)
	}

	if err := etcd.PutValueToEtcd(s.etcdClient, transferKey(t.ProviderPid), string(payload)); err != nil {
		return fmt.Errorf("saving transfer %q: %w", t.ProviderPid, err)
	}

	s.mu.Lock()
	s.cache[t.ProviderPid] = *t.clone()
	s.mu.Unlock()

	return nil
}

// GetConsumer reads a Consumer-role transfer, keyed by ConsumerPid. Same
// hot-path/etcd-fallback shape as Get, reading from consumerCache and the
// Consumer-role etcd namespace instead.
func (s *Store) GetConsumer(id string) (*TransferProcess, error) {
	s.mu.RLock()
	if t, ok := s.consumerCache[id]; ok {
		s.mu.RUnlock()
		return t.clone(), nil
	}
	s.mu.RUnlock()

	var t TransferProcess
	raw, err := etcd.GetAndUnmarshalJSON(s.etcdClient, consumerTransferKey(id), &t)
	if err != nil {
		return nil, fmt.Errorf("fetching consumer transfer %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %q", ErrTransferNotFound, id)
	}

	s.mu.Lock()
	s.consumerCache[id] = t
	s.mu.Unlock()

	return t.clone(), nil
}

// SaveConsumer writes a Consumer-role transfer, keyed by ConsumerPid. Same
// etcd-first-then-cache shape as Save, and the same reason for not using
// etcd.SaveStructToEtcd (its Fatalw would crash the process on a
// transient etcd failure).
func (s *Store) SaveConsumer(t *TransferProcess) error {
	payload, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshaling consumer transfer %q: %w", t.ConsumerPid, err)
	}

	if err := etcd.PutValueToEtcd(s.etcdClient, consumerTransferKey(t.ConsumerPid), string(payload)); err != nil {
		return fmt.Errorf("saving consumer transfer %q: %w", t.ConsumerPid, err)
	}

	s.mu.Lock()
	s.consumerCache[t.ConsumerPid] = *t.clone()
	s.mu.Unlock()

	return nil
}

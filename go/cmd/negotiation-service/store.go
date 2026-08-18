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

// agreementIndexKey is a secondary index, providerPid keyed by the real
// Agreement's own "@id" - see SaveAgreementIndex's own doc comment for why
// this exists.
func agreementIndexKey(agreementID string) string {
	return fmt.Sprintf("/dsp/negotiations/by-agreement-id/%s", agreementID)
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

// SaveAgreementIndex records providerPid under a real Agreement's own "@id"
// - a real external consumer's TransferRequestMessage carries the
// Agreement's own @id as agreementId (the DSP spec's actual convention,
// confirmed against a real external EDC consumer, issue #94), not the
// providerPid dsp-connector's own validateAgreementId originally assumed
// (docs/transfer/dsp-transfer-state-machine.md's own open question).
// Additive: the providerPid-keyed lookup this index sits alongside stays
// untouched, so an existing caller that already knows and sends the
// providerPid (DYNAMOS's own consumer role, the DSP TCK) keeps working
// unchanged - see dsp-connector's validateAgreementId, which tries that
// path first and this one only as a fallback.
func (s *Store) SaveAgreementIndex(agreementID, providerPid string) error {
	if agreementID == "" {
		return nil
	}
	if err := etcd.PutValueToEtcd(s.etcdClient, agreementIndexKey(agreementID), providerPid); err != nil {
		return fmt.Errorf("saving agreement index %q: %w", agreementID, err)
	}
	return nil
}

// GetProviderPidByAgreementID resolves a real Agreement's own "@id" to the
// providerPid of the negotiation that produced it, via SaveAgreementIndex's
// index. Returns ErrNegotiationNotFound (matching Get's own sentinel) if no
// such Agreement was ever indexed.
func (s *Store) GetProviderPidByAgreementID(agreementID string) (string, error) {
	raw, err := etcd.GetByteValueFromEtcd(s.etcdClient, agreementIndexKey(agreementID))
	if err != nil {
		return "", fmt.Errorf("fetching agreement index %q: %w", agreementID, err)
	}
	if raw == nil {
		return "", fmt.Errorf("%w: agreementId %q", ErrNegotiationNotFound, agreementID)
	}
	return string(raw), nil
}

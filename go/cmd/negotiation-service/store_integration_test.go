//go:build integration
// +build integration

package main

import (
	"os"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_Integration exercises Save/Get against a real etcd
// (docker run -p 23790:2379 quay.io/coreos/etcd:v3.5.1 ...), same convention
// as catalog-service's own integration test.
func TestStore_Integration(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

	store := NewStore(client)

	n := newNegotiation("VU", "urn:example:consumer:1", []byte(`{"@id":"offer-1"}`))
	require.NoError(t, store.Save(n))

	fetched, err := store.Get(n.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, n.ProviderPid, fetched.ProviderPid)
	assert.Equal(t, StateRequested, fetched.State)
	assert.JSONEq(t, `{"@id":"offer-1"}`, string(fetched.Offer))
}

// TestStore_Integration_HotPathSurvivesEtcdOnlyRead confirms Get serves from
// the in-memory cache without a repeat etcd round-trip - by fetching from a
// second Store instance pointed at the same etcd key (a fresh cache), then
// mutating etcd directly underneath the first, already-warm Store, and
// checking the warm Store still returns the pre-mutation value.
func TestStore_Integration_HotPathServesFromCache(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

	store := NewStore(client)
	n := newNegotiation("VU", "urn:example:consumer:2", []byte(`{}`))
	require.NoError(t, store.Save(n))

	// Warm the cache.
	_, err := store.Get(n.ProviderPid)
	require.NoError(t, err)

	// Mutate etcd directly, bypassing the Store - simulates another
	// replica's write. The warm Store's cache is now stale by design (no
	// Watch, per T2.1's decision - hot path trades consistency for no
	// etcd round-trip on every read).
	other := newNegotiation("VU", "urn:example:consumer:2", []byte(`{}`))
	other.ProviderPid = n.ProviderPid
	require.NoError(t, other.transition(StateTerminated, StateRequested))
	require.NoError(t, etcd.SaveStructToEtcd(client, negotiationKey(n.ProviderPid), other))

	stale, err := store.Get(n.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateRequested, stale.State, "cache should still serve the pre-mutation value")

	// A fresh Store (empty cache) sees the real, current etcd value.
	fresh := NewStore(client)
	live, err := fresh.Get(n.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateTerminated, live.State)
}

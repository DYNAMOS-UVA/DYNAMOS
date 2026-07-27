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

// TestStore_Integration tests Save and Get against a real etcd
// (docker run -p 23790:2379 quay.io/coreos/etcd:v3.5.1 ...). This follows
// the same convention as negotiation-service's own integration test.
func TestStore_Integration(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

	store := NewStore(client)

	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", []byte(`{"endpoint":"http://example.com"}`))
	require.NoError(t, store.Save(tp))

	fetched, err := store.Get(tp.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, tp.ProviderPid, fetched.ProviderPid)
	assert.Equal(t, StateRequested, fetched.State)
	assert.JSONEq(t, `{"endpoint":"http://example.com"}`, string(fetched.DataAddress))
}

// TestStore_Integration_HotPathServesFromCache checks that Get serves
// from the in-memory cache, with no repeat etcd round-trip. The test
// warms a Store, then changes etcd directly underneath it. The warm
// Store must still return the pre-change value. A fresh Store must see
// the live value.
func TestStore_Integration_HotPathServesFromCache(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

	store := NewStore(client)
	tp := newTransferProcess("VU", "urn:example:consumer:2", "consumer@example.com", "urn:example:agreement:2", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)
	require.NoError(t, store.Save(tp))

	// Warm the cache.
	_, err := store.Get(tp.ProviderPid)
	require.NoError(t, err)

	// This changes etcd directly, bypassing the Store. It simulates
	// another replica's write. The warm Store's cache is now stale by
	// design: it has no Watch. negotiation-service's own hot path makes
	// the same trade-off.
	other := newTransferProcess("VU", "urn:example:consumer:2", "consumer@example.com", "urn:example:agreement:2", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)
	other.ProviderPid = tp.ProviderPid
	require.NoError(t, other.transition(StateStarted, StateRequested, StateSuspended))
	require.NoError(t, etcd.SaveStructToEtcd(client, transferKey(tp.ProviderPid), other))

	stale, err := store.Get(tp.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateRequested, stale.State, "cache should still serve the pre-mutation value")

	// A fresh Store (empty cache) sees the real, current etcd value.
	fresh := NewStore(client)
	live, err := fresh.Get(tp.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateStarted, live.State)
}

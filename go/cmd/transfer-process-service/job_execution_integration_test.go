//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedDelivery struct {
	path string
	body map[string]any
}

type deliveryRecorder struct {
	mu    sync.Mutex
	url   string
	calls []capturedDelivery
}

func (r *deliveryRecorder) record(c capturedDelivery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *deliveryRecorder) all() []capturedDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capturedDelivery(nil), r.calls...)
}

// startFixtureConsumerCallback stands in for the Consumer's own DSP
// callback endpoint. It records every outbound delivery this service
// sends, so a test can check the transition and the message together.
func startFixtureConsumerCallback(t *testing.T) *deliveryRecorder {
	t.Helper()

	rec := &deliveryRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/transfers/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		rec.record(capturedDelivery{path: r.URL.Path, body: body})
		w.WriteHeader(http.StatusOK)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	rec.url = ts.URL
	return rec
}

func newIntegrationStore(t *testing.T) *Store {
	t.Helper()

	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	t.Cleanup(func() { client.Close() })
	return NewStore(client)
}

// TestTriggerJobAndDeliver_Success runs the full T3.2 happy path. A
// REQUESTED transfer gets a job pipeline call that succeeds. That
// triggers a Start delivery with the job result, then a Completion
// delivery.
func TestTriggerJobAndDeliver_Success(t *testing.T) {
	store = newIntegrationStore(t)
	consumer := startFixtureConsumerCallback(t)
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jobId":"job-1","responses":["42"]}`))
	})

	tp := newTransferProcess("VU", "urn:example:consumer:success", "consumer@example.com", "urn:example:agreement:1", "dynamos:sqlDataRequest", consumer.url, json.RawMessage(`{"query":"SELECT 1"}`))
	require.NoError(t, store.Save(tp))

	triggerJobAndDeliver(tp.ProviderPid)

	final, err := store.Get(tp.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateCompleted, final.State)
	assert.JSONEq(t, `{"jobId":"job-1","responses":["42"]}`, string(final.DataAddress))

	calls := consumer.all()
	require.Len(t, calls, 2)
	assert.Contains(t, calls[0].path, "/start")
	assert.JSONEq(t, `{"jobId":"job-1","responses":["42"]}`, mustMarshal(t, calls[0].body["dataAddress"]))
	assert.Contains(t, calls[1].path, "/completion")
}

// TestTriggerJobAndDeliver_JobFailure covers a job pipeline error: the
// transfer goes straight to TERMINATED, and only a Termination delivery
// fires. No Start ever goes out for a job that never produced data.
func TestTriggerJobAndDeliver_JobFailure(t *testing.T) {
	store = newIntegrationStore(t)
	consumer := startFixtureConsumerCallback(t)
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	tp := newTransferProcess("VU", "urn:example:consumer:failure", "consumer@example.com", "urn:example:agreement:1", "dynamos:sqlDataRequest", consumer.url, json.RawMessage(`{"query":"SELECT 1"}`))
	require.NoError(t, store.Save(tp))

	triggerJobAndDeliver(tp.ProviderPid)

	final, err := store.Get(tp.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateTerminated, final.State)

	calls := consumer.all()
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0].path, "/termination")
}

// TestTriggerJobAndDeliver_LostRaceToConsumerTermination checks a race.
// The Consumer can terminate a transfer while its job pipeline call is
// still in flight. That termination must win. The job's own late
// result must never overwrite it.
func TestTriggerJobAndDeliver_LostRaceToConsumerTermination(t *testing.T) {
	store = newIntegrationStore(t)
	consumer := startFixtureConsumerCallback(t)
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jobId":"job-1","responses":["42"]}`))
	})

	tp := newTransferProcess("VU", "urn:example:consumer:race", "consumer@example.com", "urn:example:agreement:1", "dynamos:sqlDataRequest", consumer.url, json.RawMessage(`{"query":"SELECT 1"}`))
	require.NoError(t, store.Save(tp))

	// Simulate the Consumer terminating the transfer before the job
	// pipeline call (fired directly below, not through the goroutine)
	// returns.
	require.NoError(t, tp.transition(StateTerminated, StateRequested))
	require.NoError(t, store.Save(tp))

	result, err := requestJobExecution(tp)
	require.NoError(t, err)
	markStartedThenCompleted(tp.ProviderPid, result)

	final, err := store.Get(tp.ProviderPid)
	require.NoError(t, err)
	assert.Equal(t, StateTerminated, final.State, "the Consumer's termination must survive a late job result")
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

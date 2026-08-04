//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withConsumerAutoNegotiate sets consumerAutoNegotiate for the duration of
// the calling test, restoring the previous value on cleanup - #83's manual
// endpoints only do anything meaningful when it's false (main.go's own doc
// comment), and it's a package-level var shared across every test in this
// package, so each test that needs it off must not leak that into others.
func withConsumerAutoNegotiate(t *testing.T, value bool) {
	t.Helper()
	previous := consumerAutoNegotiate
	consumerAutoNegotiate = value
	t.Cleanup(func() { consumerAutoNegotiate = previous })
}

// startFakeProviderManual stands in for a remote Provider across #83's
// explicit actions: the initiating request, a counter-request, and an
// outbound termination - startFakeProviderLifecycle's events/verification
// paths aren't needed here since consumerAutoNegotiate is off in every test
// using this fixture.
func startFakeProviderManual(t *testing.T, providerPid string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/negotiations/request", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"providerPid": providerPid, "state": "REQUESTED"})
	})
	mux.HandleFunc("/negotiations/"+providerPid+"/request", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/negotiations/"+providerPid+"/termination", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/negotiations/"+providerPid+"/agreement/verification", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestAutoAcceptOffer_DisabledLeavesOffered(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:disabled-accept")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	offerRec := doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/offer", consumerPid,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, offerRec.Code)
	assert.Equal(t, StateOffered, decodeNegotiation(t, offerRec).State)

	n, err := store.Get(KindConsumer, consumerPid)
	require.NoError(t, err)
	assert.Equal(t, StateOffered, n.State, "consumerAutoNegotiate=false must leave the negotiation at OFFERED")
	assert.Equal(t, int32(0), fixture.eventsHits.Load(), "no outbound ACCEPTED event when auto-negotiate is off")
}

func TestNegotiationConsumerAcceptHandler_Success(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:manual-accept")
	consumerPid := createConsumerNegotiation(t, provider.URL)
	doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/offer", consumerPid,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)

	rec := doRequest(negotiationConsumerAcceptHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/accept", consumerPid, "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, StateAccepted, decodeNegotiation(t, rec).State)
	assert.Equal(t, int32(1), fixture.eventsHits.Load())
}

func TestNegotiationConsumerAcceptHandler_WrongState(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider, _ := startFakeProviderLifecycle(t, "urn:example:provider:manual-accept-wrong-state")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	rec := doRequest(negotiationConsumerAcceptHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/accept", consumerPid, "")

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "invalid-transition", decodeInternalError(t, rec).Code)
}

func TestNegotiationConsumerVerifyHandler_Success(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:manual-verify")
	consumerPid := createConsumerNegotiation(t, provider.URL)
	doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/agreement", consumerPid,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)

	rec := doRequest(negotiationConsumerVerifyHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/verify", consumerPid, "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, StateVerified, decodeNegotiation(t, rec).State)
	assert.Equal(t, int32(1), fixture.verificationHits.Load())
}

func TestNegotiationConsumerCounterHandler_Success(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider := startFakeProviderManual(t, "urn:example:provider:manual-counter")
	consumerPid := createConsumerNegotiation(t, provider.URL)
	doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/offer", consumerPid,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)

	rec := doRequest(negotiationConsumerCounterHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/counter", consumerPid,
		`{"offer":{"@id":"offer-2","target":"ds-1"}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, StateRequested, decodeNegotiation(t, rec).State)
}

func TestNegotiationConsumerCounterHandler_WrongState(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider := startFakeProviderManual(t, "urn:example:provider:manual-counter-wrong-state")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	rec := doRequest(negotiationConsumerCounterHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/counter", consumerPid,
		`{"offer":{"@id":"offer-2","target":"ds-1"}}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "invalid-transition", decodeInternalError(t, rec).Code)
}

func TestNegotiationConsumerCounterHandler_MissingOffer(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider := startFakeProviderManual(t, "urn:example:provider:manual-counter-missing-offer")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	rec := doRequest(negotiationConsumerCounterHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/counter", consumerPid, `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-offer", decodeInternalError(t, rec).Code)
}

func TestNegotiationConsumerTerminateOutboundHandler_Success(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider := startFakeProviderManual(t, "urn:example:provider:manual-terminate")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	rec := doRequest(negotiationConsumerTerminateOutboundHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/terminate", consumerPid, "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, StateTerminated, decodeNegotiation(t, rec).State)
}

func TestNegotiationConsumerTerminateOutboundHandler_RejectsFromFinalized(t *testing.T) {
	wireHandlerTestStore(t)
	withConsumerAutoNegotiate(t, false)

	provider := startFakeProviderManual(t, "urn:example:provider:manual-terminate-finalized")
	consumerPid := createConsumerNegotiation(t, provider.URL)
	doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/agreement", consumerPid,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)
	doRequest(negotiationConsumerVerifyHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/verify", consumerPid, "")
	doRequest(negotiationConsumerEventsHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/events", consumerPid, `{"eventType":"FINALIZED"}`)

	rec := doRequest(negotiationConsumerTerminateOutboundHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/terminate", consumerPid, "")

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "invalid-transition", decodeInternalError(t, rec).Code)
}

func TestNegotiationConsumerAcceptHandler_WrongMethod(t *testing.T) {
	req := doRequest(negotiationConsumerAcceptHandler, http.MethodGet, "/internal/v1/negotiations/consumer/x/accept", "x", "")
	assert.Equal(t, http.StatusMethodNotAllowed, req.Code)
}

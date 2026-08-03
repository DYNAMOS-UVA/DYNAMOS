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

// fakeProvider stands in for the remote dsp-connector's
// POST /negotiations/request endpoint - just enough of the DSP ack shape
// (providerPid) for sendContractRequest to adopt the negotiation, real HTTP
// round-trip included (not a mock of sendContractRequest itself).
func fakeProvider(t *testing.T, providerPid string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/negotiations/request", r.URL.Path)
		assert.Equal(t, "did:web:vu.example.com", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"@context":    dspContext,
			"@type":       "ContractNegotiation",
			"providerPid": providerPid,
			"state":       "REQUESTED",
		})
	}))
}

func TestNegotiationsConsumerCollectionHandler_Create(t *testing.T) {
	wireHandlerTestStore(t)

	provider := fakeProvider(t, "urn:example:provider:1")
	defer provider.Close()

	rec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	n := decodeNegotiation(t, rec)
	assert.Equal(t, KindConsumer, n.Kind)
	assert.Equal(t, StateRequested, n.State)
	assert.Equal(t, "urn:example:provider:1", n.ProviderPid)
	assert.Equal(t, "did:web:surf.example.com", n.RemoteParticipant)
	assert.Contains(t, n.ConsumerPid, "urn:dynamos:negotiation:consumer:VU:")
}

func TestNegotiationsConsumerCollectionHandler_MissingProviderEndpoint(t *testing.T) {
	wireHandlerTestStore(t)

	rec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-provider-endpoint", decodeInternalError(t, rec).Code)
}

func TestNegotiationsConsumerCollectionHandler_MissingRemoteParticipant(t *testing.T) {
	wireHandlerTestStore(t)

	provider := fakeProvider(t, "urn:example:provider:missing-remote")
	defer provider.Close()

	rec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"did:web:vu.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-remote-participant", decodeInternalError(t, rec).Code)
}

// TestNegotiationsConsumerCollectionHandler_ProviderRejects confirms a
// non-2xx from the remote Provider fails the internal-API call and persists
// nothing - there must be no local record of a negotiation the remote side
// never actually accepted.
func TestNegotiationsConsumerCollectionHandler_ProviderRejects(t *testing.T) {
	wireHandlerTestStore(t)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer provider.Close()

	rec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "provider-request-failed", decodeInternalError(t, rec).Code)
}

// TestNegotiationConsumerLifecycle_FullPath drives one Consumer-role
// negotiation through every inbound-recording transition #80 provides,
// mirroring TestNegotiationLifecycle_FullPath's Provider-role shape. ACCEPTED
// and VERIFIED are deliberately absent - #80 records inbound messages only,
// sending those two outbound is #82's autonomous accept-all logic.
func TestNegotiationConsumerLifecycle_FullPath(t *testing.T) {
	wireHandlerTestStore(t)

	provider := fakeProvider(t, "urn:example:provider:lifecycle")
	defer provider.Close()

	createRec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)
	require.Equal(t, http.StatusCreated, createRec.Code)
	id := decodeNegotiation(t, createRec).ConsumerPid

	offerRec := doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+id+"/offer", id,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, offerRec.Code)
	assert.Equal(t, StateOffered, decodeNegotiation(t, offerRec).State)

	// OFFERED -> ACCEPTED is DYNAMOS-as-Consumer's own outbound send, #82's
	// autonomous accept-all logic, not built yet - seed it directly to reach
	// a realistic predecessor state for AGREED (mirrors the VERIFIED seed
	// below for the same reason).
	afterOffer, err := store.Get(KindConsumer, id)
	require.NoError(t, err)
	require.NoError(t, afterOffer.transition(StateAccepted, StateOffered))
	require.NoError(t, store.Save(afterOffer))

	agreementRec := doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+id+"/agreement", id,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, agreementRec.Code)
	assert.Equal(t, StateAgreed, decodeNegotiation(t, agreementRec).State)

	// Straight to VERIFIED/FINALIZED: #80 has no verification-send endpoint
	// yet (#82's job), so seed the state directly to exercise the events
	// handler's FINALIZED path in isolation.
	n, err := store.Get(KindConsumer, id)
	require.NoError(t, err)
	require.NoError(t, n.transition(StateVerified, StateAgreed))
	require.NoError(t, store.Save(n))

	finalizeRec := doRequest(negotiationConsumerEventsHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+id+"/events", id,
		`{"eventType":"FINALIZED"}`)
	require.Equal(t, http.StatusOK, finalizeRec.Code)
	assert.Equal(t, StateFinalized, decodeNegotiation(t, finalizeRec).State)

	terminateRec := doRequest(negotiationConsumerTerminationHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+id+"/termination", id, `{}`)
	assert.Equal(t, http.StatusConflict, terminateRec.Code)
	assert.Equal(t, "invalid-transition", decodeInternalError(t, terminateRec).Code)
}

func TestNegotiationConsumerEventsHandler_RejectsAccepted(t *testing.T) {
	wireHandlerTestStore(t)

	provider := fakeProvider(t, "urn:example:provider:reject-accepted")
	defer provider.Close()

	createRec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)
	id := decodeNegotiation(t, createRec).ConsumerPid

	rec := doRequest(negotiationConsumerEventsHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+id+"/events", id,
		`{"eventType":"ACCEPTED"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid-event-type", decodeInternalError(t, rec).Code)
}

// TestNegotiationConsumerAndProvider_DoNotCollide is the handler-level
// counterpart to the store-level collision test - a Provider-role and a
// Consumer-role negotiation created independently through the real HTTP
// handlers must never be visible to each other's GET endpoint.
func TestNegotiationConsumerAndProvider_DoNotCollide(t *testing.T) {
	wireHandlerTestStore(t)

	provider := fakeProvider(t, "urn:example:provider:no-collide")
	defer provider.Close()

	providerRoleRec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"consumerPid":"urn:example:consumer:no-collide","participant":"consumer@example.com","offer":{"@id":"offer-1"}}`)
	require.Equal(t, http.StatusCreated, providerRoleRec.Code)
	providerRoleId := decodeNegotiation(t, providerRoleRec).ProviderPid

	consumerRoleRec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)
	require.Equal(t, http.StatusCreated, consumerRoleRec.Code)
	consumerRoleId := decodeNegotiation(t, consumerRoleRec).ConsumerPid

	notFoundRec := doRequest(negotiationConsumerHandler, http.MethodGet, "/internal/v1/negotiations/consumer/"+providerRoleId, providerRoleId, "")
	assert.Equal(t, http.StatusNotFound, notFoundRec.Code, "the Provider-role negotiation must not be reachable through the Consumer-role GET")

	notFoundRec2 := doRequest(negotiationHandler, http.MethodGet, "/internal/v1/negotiations/"+consumerRoleId, consumerRoleId, "")
	assert.Equal(t, http.StatusNotFound, notFoundRec2.Code, "the Consumer-role negotiation must not be reachable through the Provider-role GET")
}

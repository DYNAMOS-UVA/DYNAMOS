//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireHandlerTestStore wires the package-level etcdClient/party/store
// (normally set in main()) against a real etcd, same convention as
// catalog-service's seedHandlerTestData.
func wireHandlerTestStore(t *testing.T) {
	t.Helper()

	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23791"
	}
	etcdClient = etcd.GetEtcdClient(endpoint)
	party = "VU"
	store = NewStore(etcdClient)
}

func doRequest(h http.HandlerFunc, method, target, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeNegotiation(t *testing.T, rec *httptest.ResponseRecorder) *Negotiation {
	t.Helper()
	var n Negotiation
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &n))
	return &n
}

func decodeInternalError(t *testing.T, rec *httptest.ResponseRecorder) internalError {
	t.Helper()
	var ie internalError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
	return ie
}

func TestNegotiationsCollectionHandler_Create(t *testing.T) {
	wireHandlerTestStore(t)

	rec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"consumerPid":"urn:example:consumer:1","offer":{"@id":"offer-1"}}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	n := decodeNegotiation(t, rec)
	assert.Equal(t, StateRequested, n.State)
	assert.Contains(t, n.ProviderPid, "urn:dynamos:negotiation:VU:")
}

func TestNegotiationsCollectionHandler_MissingConsumerPid(t *testing.T) {
	wireHandlerTestStore(t)

	rec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"offer":{"@id":"offer-1"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-consumer-pid", decodeInternalError(t, rec).Code)
}

func TestNegotiationsCollectionHandler_MissingOffer(t *testing.T) {
	wireHandlerTestStore(t)

	rec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"consumerPid":"urn:example:consumer:1"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-offer", decodeInternalError(t, rec).Code)
}

func TestNegotiationHandler_NotFound(t *testing.T) {
	wireHandlerTestStore(t)

	rec := doRequest(negotiationHandler, http.MethodGet, "/internal/v1/negotiations/urn:dynamos:negotiation:VU:missing",
		"urn:dynamos:negotiation:VU:missing", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "negotiation-not-found", decodeInternalError(t, rec).Code)
}

// TestNegotiationLifecycle_FullPath drives one negotiation through every
// transition end to end over the real HTTP handlers (not just Store), the
// same shape catalog-service's own handler-integration tests exercise.
func TestNegotiationLifecycle_FullPath(t *testing.T) {
	wireHandlerTestStore(t)

	createRec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"consumerPid":"urn:example:consumer:1","offer":{"@id":"offer-1"}}`)
	require.Equal(t, http.StatusCreated, createRec.Code)
	id := decodeNegotiation(t, createRec).ProviderPid

	offerRec := doRequest(negotiationOfferHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/offer", id,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, offerRec.Code)
	assert.Equal(t, StateOffered, decodeNegotiation(t, offerRec).State)

	acceptRec := doRequest(negotiationEventsHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/events", id,
		`{"eventType":"ACCEPTED"}`)
	require.Equal(t, http.StatusOK, acceptRec.Code)
	assert.Equal(t, StateAccepted, decodeNegotiation(t, acceptRec).State)

	agreementRec := doRequest(negotiationAgreementHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/agreement", id,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, agreementRec.Code)
	assert.Equal(t, StateAgreed, decodeNegotiation(t, agreementRec).State)

	verifyRec := doRequest(negotiationVerificationHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/agreement/verification", id, `{}`)
	require.Equal(t, http.StatusOK, verifyRec.Code)
	assert.Equal(t, StateVerified, decodeNegotiation(t, verifyRec).State)

	finalizeRec := doRequest(negotiationEventsHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/events", id,
		`{"eventType":"FINALIZED"}`)
	require.Equal(t, http.StatusOK, finalizeRec.Code)
	assert.Equal(t, StateFinalized, decodeNegotiation(t, finalizeRec).State)

	terminateRec := doRequest(negotiationTerminationHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/termination", id, `{}`)
	require.Equal(t, http.StatusOK, terminateRec.Code)
	assert.Equal(t, StateTerminated, decodeNegotiation(t, terminateRec).State)

	// TERMINATED is a dead end - any further write is rejected.
	afterRec := doRequest(negotiationOfferHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/offer", id,
		`{"offer":{"@id":"offer-1"}}`)
	assert.Equal(t, http.StatusConflict, afterRec.Code)
	assert.Equal(t, "invalid-transition", decodeInternalError(t, afterRec).Code)
}

func TestNegotiationEventsHandler_WrongEventType(t *testing.T) {
	wireHandlerTestStore(t)

	createRec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"consumerPid":"urn:example:consumer:2","offer":{"@id":"offer-1"}}`)
	id := decodeNegotiation(t, createRec).ProviderPid

	rec := doRequest(negotiationEventsHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/events", id,
		`{"eventType":"NOT_A_REAL_EVENT"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid-event-type", decodeInternalError(t, rec).Code)
}

func TestNegotiationAgreementHandler_WrongSourceState(t *testing.T) {
	wireHandlerTestStore(t)

	createRec := doRequest(negotiationsCollectionHandler, http.MethodPost, "/internal/v1/negotiations", "",
		`{"consumerPid":"urn:example:consumer:3","offer":{"@id":"offer-1"}}`)
	id := decodeNegotiation(t, createRec).ProviderPid

	// Still REQUESTED - agreement is only valid from ACCEPTED.
	rec := doRequest(negotiationAgreementHandler, http.MethodPost, "/internal/v1/negotiations/"+id+"/agreement", id,
		`{"agreement":{"@id":"agr-1"}}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "invalid-transition", decodeInternalError(t, rec).Code)
}

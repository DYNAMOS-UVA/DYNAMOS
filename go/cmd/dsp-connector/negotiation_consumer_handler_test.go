package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFixtureConsumerNegotiationService stands in for negotiation-service's
// Consumer-role internal API (#80), same shape as
// startFixtureNegotiationService but for the routes negotiation_consumer_client.go
// calls. remoteParticipant defaults to the identity every test authenticates
// its fake remote Provider as.
func startFixtureConsumerNegotiationService(t *testing.T) (state, remoteParticipant *string) {
	t.Helper()

	s := "REQUESTED"
	rp := "did:web:surf.example.com"
	const providerPid = "urn:example:provider:fixture-1"
	const consumerPid = "urn:dynamos:negotiation:consumer:VU:fixture-1"

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != consumerPid {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "negotiation-not-found", "error": "no negotiation found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "remoteParticipant": rp, "state": s})
	})
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/offer", func(w http.ResponseWriter, r *http.Request) {
		s = "OFFERED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "remoteParticipant": rp, "state": s})
	})
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/agreement", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s != "ACCEPTED" && s != "REQUESTED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		s = "AGREED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "remoteParticipant": rp, "state": s})
	})
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s != "VERIFIED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		s = "FINALIZED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "remoteParticipant": rp, "state": s})
	})
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/termination", func(w http.ResponseWriter, r *http.Request) {
		s = "TERMINATED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "remoteParticipant": rp, "state": s})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL
	return &s, &rp
}

const fixtureConsumerPid = "urn:dynamos:negotiation:consumer:VU:fixture-1"
const fixtureProviderPid = "urn:example:provider:fixture-1"

func consumerOfferBody() string {
	return `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractOfferMessage","providerPid":"` + fixtureProviderPid + `","consumerPid":"` + fixtureConsumerPid + `","offer":{"@type":"Offer","@id":"urn:dynamos:offer:VU:GUID","target":"urn:dynamos:dataset:VU:wageGap"}}`
}

// TestNegotiationConsumerGetHandler_Success covers #83's status-poll
// endpoint (GET /:callback/negotiations/:consumerPid), not one of the DSP
// spec's own Consumer Path Bindings - see the handler's own doc comment.
func TestNegotiationConsumerGetHandler_Success(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/callback/negotiations/"+fixtureConsumerPid, nil)
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerGetHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "REQUESTED", ack.State)
}

func TestNegotiationConsumerGetHandler_WrongParticipant(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/callback/negotiations/"+fixtureConsumerPid, nil)
	req.Header.Set("Authorization", testAuthHeader("did:web:someone-else.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerGetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationConsumerGetHandler_NotFound(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/callback/negotiations/urn:dynamos:negotiation:consumer:VU:missing", nil)
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", "urn:dynamos:negotiation:consumer:VU:missing")
	rec := httptest.NewRecorder()

	negotiationConsumerGetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationConsumerGetHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid, nil)
	rec := httptest.NewRecorder()

	negotiationConsumerGetHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestNegotiationConsumerOffersHandler_Success(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/offers", bytes.NewBufferString(consumerOfferBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerOffersHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "OFFERED", ack.State)
}

func TestNegotiationConsumerOffersHandler_MissingAuthorization(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/offers", bytes.NewBufferString(consumerOfferBody()))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerOffersHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

func TestNegotiationConsumerOffersHandler_MissingOffer(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/offers",
		bytes.NewBufferString(`{"providerPid":"`+fixtureProviderPid+`","consumerPid":"`+fixtureConsumerPid+`"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerOffersHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationConsumerOffersHandler_WrongParticipant is #81's IDOR check
// (mirrors TestNegotiationGetHandler_WrongParticipant): a caller who isn't
// the declared RemoteParticipant for this negotiation must be rejected,
// same as T2.3's Provider-role ownership check.
func TestNegotiationConsumerOffersHandler_WrongParticipant(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/offers", bytes.NewBufferString(consumerOfferBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:someone-else.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerOffersHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationConsumerOffersHandler_NotFound(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/urn:dynamos:negotiation:consumer:VU:missing/offers", bytes.NewBufferString(consumerOfferBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", "urn:dynamos:negotiation:consumer:VU:missing")
	rec := httptest.NewRecorder()

	negotiationConsumerOffersHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationConsumerAgreementHandler_Success(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	body := `{"providerPid":"` + fixtureProviderPid + `","consumerPid":"` + fixtureConsumerPid + `","agreement":{"@id":"agr-1","target":"urn:dynamos:dataset:VU:wageGap"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/agreement", bytes.NewBufferString(body))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerAgreementHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "AGREED", ack.State)
}

func TestNegotiationConsumerAgreementHandler_MissingAgreement(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/agreement",
		bytes.NewBufferString(`{"providerPid":"`+fixtureProviderPid+`","consumerPid":"`+fixtureConsumerPid+`"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerAgreementHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

func TestNegotiationConsumerEventsHandler_WrongEventType(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED","providerPid":"`+fixtureProviderPid+`","consumerPid":"`+fixtureConsumerPid+`"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-event-type", ne.Code)
}

func TestNegotiationConsumerEventsHandler_InvalidTransition(t *testing.T) {
	// Fixture starts at REQUESTED - FINALIZED is only valid from VERIFIED.
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/events",
		bytes.NewBufferString(`{"eventType":"FINALIZED","providerPid":"`+fixtureProviderPid+`","consumerPid":"`+fixtureConsumerPid+`"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-transition", ne.Code)
}

func TestNegotiationConsumerTerminationHandler_Success(t *testing.T) {
	startFixtureConsumerNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/termination",
		bytes.NewBufferString(`{"providerPid":"`+fixtureProviderPid+`","consumerPid":"`+fixtureConsumerPid+`","code":"99","reason":["License model does not fit."]}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:surf.example.com"))
	req.SetPathValue("consumerPid", fixtureConsumerPid)
	rec := httptest.NewRecorder()

	negotiationConsumerTerminationHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "TERMINATED", ack.State)
}

func TestNegotiationConsumerOffersHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/callback/negotiations/"+fixtureConsumerPid+"/offers", nil)
	rec := httptest.NewRecorder()

	negotiationConsumerOffersHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

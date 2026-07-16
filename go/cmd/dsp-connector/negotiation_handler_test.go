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

// startFixtureNegotiationService stands in for a real negotiation-service,
// same shape as catalog_handler_test.go's startFixtureCatalogService. Tracks
// one in-memory negotiation so lifecycle handlers behave consistently
// across a test.
func startFixtureNegotiationService(t *testing.T) *string {
	t.Helper()

	state := "REQUESTED"
	const providerPid = "urn:dynamos:negotiation:VU:fixture-1"
	const consumerPid = "urn:example:consumer:1"

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != providerPid {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "negotiation-not-found", "error": "no negotiation found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/request", func(w http.ResponseWriter, r *http.Request) {
		state = "REQUESTED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EventType string `json:"eventType"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if state != "OFFERED" && body.EventType == "ACCEPTED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		state = body.EventType
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/agreement/verification", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		state = "VERIFIED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/termination", func(w http.ResponseWriter, r *http.Request) {
		state = "TERMINATED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "state": state})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL
	return &state
}

func offerBody(offerID string) string {
	return `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractRequestMessage","consumerPid":"urn:example:consumer:1","offer":{"@type":"Offer","@id":"` + offerID + `","target":"urn:dynamos:dataset:VU:wageGap","permission":[{"action":"use"}]}}`
}

func TestNegotiationRequestInitHandler_ValidOffer(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	req.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
	rec := httptest.NewRecorder()

	negotiationRequestInitHandler(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "ContractNegotiation", ack.Type)
	assert.Equal(t, "REQUESTED", ack.State)
}

func TestNegotiationRequestInitHandler_UnknownOffer(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:doesnotexist")))
	req.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
	rec := httptest.NewRecorder()

	negotiationRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-offer", ne.Code)
}

func TestNegotiationRequestInitHandler_MissingAuthorization(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	rec := httptest.NewRecorder()

	negotiationRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

func TestNegotiationRequestInitHandler_MissingConsumerPid(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(`{"offer":{"@id":"urn:dynamos:offer:VU:GUID"}}`))
	req.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
	rec := httptest.NewRecorder()

	negotiationRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

func TestNegotiationRequestInitHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/request", nil)
	rec := httptest.NewRecorder()

	negotiationRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestNegotiationGetHandler_Found(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1", nil)
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationGetHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "urn:dynamos:negotiation:VU:fixture-1", ack.ProviderPid)
}

func TestNegotiationGetHandler_NotFound(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/urn:dynamos:negotiation:VU:missing", nil)
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:missing")
	rec := httptest.NewRecorder()

	negotiationGetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationEventsHandler_WrongEventType(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events",
		bytes.NewBufferString(`{"eventType":"FINALIZED","providerPid":"urn:dynamos:negotiation:VU:fixture-1","consumerPid":"urn:example:consumer:1"}`))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-event-type", ne.Code)
}

func TestNegotiationEventsHandler_InvalidTransition(t *testing.T) {
	// Fixture starts at REQUESTED - ACCEPTED is only valid from OFFERED.
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED","providerPid":"urn:dynamos:negotiation:VU:fixture-1","consumerPid":"urn:example:consumer:1"}`))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-transition", ne.Code)
}

// TestNegotiationLifecycle_FullPath drives one negotiation through every
// provider endpoint end to end over the real HTTP handlers, mirroring
// negotiation-service's own httptest lifecycle test one layer up the stack.
func TestNegotiationLifecycle_FullPath(t *testing.T) {
	startFixtureCatalogService(t)
	statePtr := startFixtureNegotiationService(t)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	createReq.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
	createRec := httptest.NewRecorder()
	negotiationRequestInitHandler(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)
	var created negotiationAck
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))
	providerPid := created.ProviderPid

	// Move the fixture to OFFERED (a real dsp-connector never does this
	// directly - the Provider sends the Offer out-of-band - but the fixture
	// needs it to exercise the events/ACCEPTED transition below).
	*statePtr = "OFFERED"

	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/"+providerPid+"/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED","providerPid":"`+providerPid+`","consumerPid":"urn:example:consumer:1"}`))
	acceptReq.SetPathValue("providerPid", providerPid)
	acceptRec := httptest.NewRecorder()
	negotiationEventsHandler(acceptRec, acceptReq)
	require.Equal(t, http.StatusOK, acceptRec.Code)

	verifyReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/"+providerPid+"/agreement/verification", bytes.NewBufferString(`{}`))
	verifyReq.SetPathValue("providerPid", providerPid)
	verifyRec := httptest.NewRecorder()
	negotiationVerificationHandler(verifyRec, verifyReq)
	require.Equal(t, http.StatusOK, verifyRec.Code)
	var verified negotiationAck
	require.NoError(t, json.Unmarshal(verifyRec.Body.Bytes(), &verified))
	assert.Equal(t, "VERIFIED", verified.State)

	terminateReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/"+providerPid+"/termination", bytes.NewBufferString(`{}`))
	terminateReq.SetPathValue("providerPid", providerPid)
	terminateRec := httptest.NewRecorder()
	negotiationTerminationHandler(terminateRec, terminateReq)
	require.Equal(t, http.StatusOK, terminateRec.Code)
	var terminated negotiationAck
	require.NoError(t, json.Unmarshal(terminateRec.Body.Bytes(), &terminated))
	assert.Equal(t, "TERMINATED", terminated.State)
}

func TestValidateOffer_TargetMismatch(t *testing.T) {
	startFixtureCatalogService(t)

	body := `{"@id":"urn:dynamos:offer:VU:GUID","target":"urn:dynamos:dataset:VU:somethingElse"}`
	err := validateOffer("jorrit.stutterheim@cloudnation.nl", json.RawMessage(body))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidOffer)
}

func TestValidateOffer_MissingID(t *testing.T) {
	err := validateOffer("jorrit.stutterheim@cloudnation.nl", json.RawMessage(`{"target":"urn:dynamos:dataset:VU:wageGap"}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidOffer)
}

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

// fixtureNegotiationService is the *string pair startFixtureNegotiationService
// hands back so a test can reach into the fixture's in-memory negotiation the
// same way TestNegotiationLifecycle_FullPath already pokes state directly -
// participant lets a test simulate "this negotiation belongs to someone
// else" (or "this negotiation's owner has since been de-provisioned")
// without needing a second fixture negotiation.
type fixtureNegotiationService struct {
	state       *string
	participant *string
}

// startFixtureNegotiationService stands in for a real negotiation-service,
// same shape as catalog_handler_test.go's startFixtureCatalogService. Tracks
// one in-memory negotiation so lifecycle handlers behave consistently
// across a test. participant defaults to the same identity every other
// fixture (startFixtureCatalogService) and most tests authenticate as.
func startFixtureNegotiationService(t *testing.T) fixtureNegotiationService {
	t.Helper()

	state := "REQUESTED"
	participant := "jorrit.stutterheim@cloudnation.nl"
	const providerPid = "urn:dynamos:negotiation:VU:fixture-1"
	const consumerPid = "urn:example:consumer:1"

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Participant string `json:"participant"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Participant != "" {
			participant = body.Participant
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "participant": participant, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != providerPid {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "negotiation-not-found", "error": "no negotiation found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "participant": participant, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/request", func(w http.ResponseWriter, r *http.Request) {
		state = "REQUESTED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "participant": participant, "state": state})
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
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "participant": participant, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/agreement/verification", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		state = "VERIFIED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "participant": participant, "state": state})
	})
	mux.HandleFunc("/internal/v1/negotiations/{id}/termination", func(w http.ResponseWriter, r *http.Request) {
		state = "TERMINATED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"providerPid": providerPid, "consumerPid": consumerPid, "participant": participant, "state": state})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL
	return fixtureNegotiationService{state: &state, participant: &participant}
}

func offerBody(offerID string) string {
	return `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractRequestMessage","consumerPid":"urn:example:consumer:1","callbackAddress":"https://consumer.example.com/callback","offer":{"@type":"Offer","@id":"` + offerID + `","target":"urn:dynamos:dataset:VU:wageGap","permission":[{"action":"use"}]}}`
}

func TestNegotiationRequestInitHandler_ValidOffer(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
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
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
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
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	rec := httptest.NewRecorder()

	negotiationRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationRequestInitHandler_MissingProviderPidAndCallback covers the
// ContractRequestMessage schema's oneOf{callbackAddress, providerPid} - a
// message with neither must be rejected as invalid, not silently accepted.
func TestNegotiationRequestInitHandler_MissingProviderPidAndCallback(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request",
		bytes.NewBufferString(`{"consumerPid":"urn:example:consumer:1","offer":{"@id":"urn:dynamos:offer:VU:GUID"}}`))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
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
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
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
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:missing")
	rec := httptest.NewRecorder()

	negotiationGetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationGetHandler_MissingAuthorization(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1", nil)
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationGetHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

// TestNegotiationGetHandler_WrongParticipant covers the IDOR gap: an
// authenticated participant who never opened this negotiation must get the
// same 404 as an unknown providerPid, not a peek at someone else's state.
func TestNegotiationGetHandler_WrongParticipant(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1", nil)
	req.Header.Set("Authorization", testAuthHeader("someone-else@example.com"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
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
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
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
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-transition", ne.Code)
}

func TestNegotiationEventsHandler_EmptyBody(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events", nil)
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code, "an empty/missing body must be reported as invalid-request, not invalid-event-type")
}

func TestNegotiationEventsHandler_MissingConsumerPid(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED"}`))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationEventsHandler_ConsumerPidMismatch: a well-formed ACCEPTED
// event whose consumerPid doesn't match the negotiation's own record must be
// rejected, not silently accepted against the wrong consumerPid.
func TestNegotiationEventsHandler_ConsumerPidMismatch(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED","consumerPid":"urn:example:consumer:wrong"}`))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationEventsHandler_WrongParticipant covers the IDOR gap for the
// events endpoint - same expectation as the Get handler's equivalent test.
func TestNegotiationEventsHandler_WrongParticipant(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED","consumerPid":"urn:example:consumer:1"}`))
	req.Header.Set("Authorization", testAuthHeader("someone-else@example.com"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationEventsHandler_MissingAuthorization(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED"}`))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationEventsHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

func TestNegotiationVerificationHandler_MissingAuthorization(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/agreement/verification", bytes.NewBufferString(`{}`))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationVerificationHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

// TestNegotiationVerificationHandler_WrongParticipant covers the IDOR gap
// for the verification endpoint - same expectation as Get's equivalent test.
func TestNegotiationVerificationHandler_WrongParticipant(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/agreement/verification", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", testAuthHeader("someone-else@example.com"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationVerificationHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationTerminationHandler_MissingAuthorization(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/termination", bytes.NewBufferString(`{}`))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationTerminationHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

// TestNegotiationTerminationHandler_WrongParticipant covers the IDOR gap for
// the termination endpoint - same expectation as Get's equivalent test.
func TestNegotiationTerminationHandler_WrongParticipant(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/termination",
		bytes.NewBufferString(`{"consumerPid":"urn:example:consumer:1"}`))
	req.Header.Set("Authorization", testAuthHeader("someone-else@example.com"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationTerminationHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

// TestNegotiationTerminationHandler_MissingConsumerPid: consumerPid is
// required per the ContractNegotiationTerminationMessage schema - a bare
// `{}` must now be rejected (this used to be accepted; tightened to match
// the spec).
func TestNegotiationTerminationHandler_MissingConsumerPid(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/termination", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationTerminationHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationTerminationHandler_ConsumerPidMismatch: a well-formed
// termination whose consumerPid doesn't match the negotiation's own record
// must be rejected, not silently applied to the wrong consumerPid.
func TestNegotiationTerminationHandler_ConsumerPidMismatch(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/termination",
		bytes.NewBufferString(`{"consumerPid":"urn:example:consumer:wrong"}`))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationTerminationHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationRequestHandler_ValidCounterRequest exercises the
// counter-request endpoint directly - previously only reachable via its
// fixture route, never actually called by a test.
func TestNegotiationRequestHandler_ValidCounterRequest(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/request",
		bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationRequestHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "REQUESTED", ack.State)
}

// TestNegotiationRequestHandler_UnknownOfferAccepted is T2.5's DSP TCK
// finding (CN:01-02): unlike the initiating request, a counter-offer's own
// terms aren't validated against the real catalog - a real DSP counter-offer
// proposes new terms the provider hasn't necessarily offered yet, and per
// the spec's own sequence for this scenario, the provider ACKs the protocol
// message (REQUESTED) and judges its substance afterward via a real Offer
// or Termination, not a blanket catalog-match gate. This used to return 400
// invalid-offer here; requiring every counter-offer to already exist in the
// catalog isn't part of the spec and blocked a real, conformant flow.
func TestNegotiationRequestHandler_UnknownOfferAccepted(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/request",
		bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:doesnotexist")))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationRequestHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "REQUESTED", ack.State)
}

// TestNegotiationRequestHandler_ProviderPidMismatch covers self-consistency
// between the body's providerPid and the URL's - a real DSP counter-request
// echoes providerPid in the body per the schema, and it must agree with the
// path it's actually being sent to.
func TestNegotiationRequestHandler_ProviderPidMismatch(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractRequestMessage","consumerPid":"urn:example:consumer:1","providerPid":"urn:dynamos:negotiation:VU:some-other-negotiation","offer":{"@type":"Offer","@id":"urn:dynamos:offer:VU:GUID","target":"urn:dynamos:dataset:VU:wageGap","permission":[{"action":"use"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/request", bytes.NewBufferString(body))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationRequestHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

// TestNegotiationRequestHandler_WrongParticipant covers the real IDOR case:
// a stranger who never owned this negotiation must be told it doesn't exist,
// not given any error that confirms it does.
func TestNegotiationRequestHandler_WrongParticipant(t *testing.T) {
	startFixtureCatalogService(t)
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/request",
		bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	req.Header.Set("Authorization", testAuthHeader("someone-else@example.com"))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationRequestHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "not-found", ne.Code)
}

func TestNegotiationRequestHandler_MissingAuthorization(t *testing.T) {
	startFixtureNegotiationService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/request",
		bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationRequestHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestNegotiationRequestHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/urn:dynamos:negotiation:VU:fixture-1/request", nil)
	req.SetPathValue("providerPid", "urn:dynamos:negotiation:VU:fixture-1")
	rec := httptest.NewRecorder()

	negotiationRequestHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// TestNegotiationLifecycle_FullPath drives one negotiation through every
// provider endpoint end to end over the real HTTP handlers, mirroring
// negotiation-service's own httptest lifecycle test one layer up the stack.
func TestNegotiationLifecycle_FullPath(t *testing.T) {
	startFixtureCatalogService(t)
	fixture := startFixtureNegotiationService(t)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/request", bytes.NewBufferString(offerBody("urn:dynamos:offer:VU:GUID")))
	createReq.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	createRec := httptest.NewRecorder()
	negotiationRequestInitHandler(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)
	var created negotiationAck
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))
	providerPid := created.ProviderPid

	// Move the fixture to OFFERED (a real dsp-connector never does this
	// directly - the Provider sends the Offer out-of-band - but the fixture
	// needs it to exercise the events/ACCEPTED transition below).
	*fixture.state = "OFFERED"

	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/"+providerPid+"/events",
		bytes.NewBufferString(`{"eventType":"ACCEPTED","providerPid":"`+providerPid+`","consumerPid":"urn:example:consumer:1"}`))
	acceptReq.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	acceptReq.SetPathValue("providerPid", providerPid)
	acceptRec := httptest.NewRecorder()
	negotiationEventsHandler(acceptRec, acceptReq)
	require.Equal(t, http.StatusOK, acceptRec.Code)

	verifyReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/"+providerPid+"/agreement/verification", bytes.NewBufferString(`{}`))
	verifyReq.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	verifyReq.SetPathValue("providerPid", providerPid)
	verifyRec := httptest.NewRecorder()
	negotiationVerificationHandler(verifyRec, verifyReq)
	require.Equal(t, http.StatusOK, verifyRec.Code)
	var verified negotiationAck
	require.NoError(t, json.Unmarshal(verifyRec.Body.Bytes(), &verified))
	assert.Equal(t, "VERIFIED", verified.State)

	terminateReq := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/"+providerPid+"/termination", bytes.NewBufferString(`{"consumerPid":"urn:example:consumer:1"}`))
	terminateReq.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
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

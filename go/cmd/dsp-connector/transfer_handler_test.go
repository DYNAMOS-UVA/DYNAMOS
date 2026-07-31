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

const (
	transferFixtureParticipant  = "jorrit.stutterheim@cloudnation.nl"
	transferFixtureAgreementId  = "urn:dynamos:negotiation:VU:fixture-agreement"
	transferFixtureConsumerPid  = "urn:example:consumer:1"
	transferFixtureProviderPid  = "urn:dynamos:transfer:VU:fixture-1"
	transferFixtureCallbackAddr = "https://consumer.example.com/callback"
)

// startFixtureNegotiationForAgreement stands in for negotiation-service,
// serving one fixed negotiation at transferFixtureAgreementId. A test picks
// its state and owning participant, matching how startFixtureNegotiationService
// (negotiation_handler_test.go) tracks one in-memory negotiation.
func startFixtureNegotiationForAgreement(t *testing.T, state, participant string) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != transferFixtureAgreementId {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "negotiation-not-found", "error": "no negotiation found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"providerPid": transferFixtureAgreementId,
			"consumerPid": transferFixtureConsumerPid,
			"participant": participant,
			"state":       state,
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL
}

// fixtureTransferService is the *string pair startFixtureTransferService
// hands back, same shape as fixtureNegotiationService. A test can use it
// to drive the fixture's state directly, for example to put it in
// SUSPENDED before it calls transferStartHandler.
type fixtureTransferService struct {
	state       *string
	participant *string
}

// startFixtureTransferService stands in for transfer-process-service, same
// shape as startFixtureNegotiationService. Tracks one in-memory transfer so
// lifecycle handlers behave consistently across a test.
func startFixtureTransferService(t *testing.T) fixtureTransferService {
	t.Helper()

	state := "REQUESTED"
	participant := transferFixtureParticipant

	record := func() map[string]string {
		return map[string]string{
			"providerPid": transferFixtureProviderPid,
			"consumerPid": transferFixtureConsumerPid,
			"participant": participant,
			"agreementId": transferFixtureAgreementId,
			"state":       state,
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/transfers", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Participant string `json:"participant"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Participant != "" {
			participant = body.Participant
		}
		state = "REQUESTED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != transferFixtureProviderPid {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "transfer-not-found", "error": "no transfer found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if state != "REQUESTED" && state != "SUSPENDED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		state = "STARTED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/{id}/completion", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if state != "STARTED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		state = "COMPLETED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/{id}/suspension", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if state != "STARTED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		state = "SUSPENDED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/{id}/termination", func(w http.ResponseWriter, r *http.Request) {
		state = "TERMINATED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	transferServiceURL = ts.URL
	return fixtureTransferService{state: &state, participant: &participant}
}

func transferRequestBodyJSON() string {
	return `{"consumerPid":"` + transferFixtureConsumerPid + `","agreementId":"` + transferFixtureAgreementId + `","format":"example:HTTP_PULL","callbackAddress":"` + transferFixtureCallbackAddr + `"}`
}

func TestTransferRequestInitHandler_ValidAgreement(t *testing.T) {
	startFixtureNegotiationForAgreement(t, "FINALIZED", transferFixtureParticipant)
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/request", bytes.NewBufferString(transferRequestBodyJSON()))
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferRequestInitHandler(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "TransferProcess", ack.Type)
	assert.Equal(t, "REQUESTED", ack.State)
}

func TestTransferRequestInitHandler_AgreementNotFinalized(t *testing.T) {
	startFixtureNegotiationForAgreement(t, "AGREED", transferFixtureParticipant)
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/request", bytes.NewBufferString(transferRequestBodyJSON()))
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-agreement", te.Code)
}

func TestTransferRequestInitHandler_AgreementNotOwned(t *testing.T) {
	startFixtureNegotiationForAgreement(t, "FINALIZED", "someone-else@example.com")
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/request", bytes.NewBufferString(transferRequestBodyJSON()))
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-agreement", te.Code)
}

func TestTransferRequestInitHandler_UnknownAgreement(t *testing.T) {
	startFixtureNegotiationForAgreement(t, "FINALIZED", transferFixtureParticipant)
	startFixtureTransferService(t)

	body := `{"consumerPid":"urn:example:consumer:1","agreementId":"urn:dynamos:negotiation:VU:doesnotexist","format":"example:HTTP_PULL","callbackAddress":"https://consumer.example.com/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/request", bytes.NewBufferString(body))
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-agreement", te.Code)
}

func TestTransferRequestInitHandler_MissingAuthorization(t *testing.T) {
	startFixtureNegotiationForAgreement(t, "FINALIZED", transferFixtureParticipant)
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/request", bytes.NewBufferString(transferRequestBodyJSON()))
	rec := httptest.NewRecorder()

	transferRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestTransferRequestInitHandler_MissingFormat(t *testing.T) {
	startFixtureNegotiationForAgreement(t, "FINALIZED", transferFixtureParticipant)
	startFixtureTransferService(t)

	body := `{"consumerPid":"urn:example:consumer:1","agreementId":"` + transferFixtureAgreementId + `","callbackAddress":"https://consumer.example.com/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/request", bytes.NewBufferString(body))
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferRequestInitHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-request", te.Code)
}

func TestTransferGetHandler_Owned(t *testing.T) {
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transfers/"+transferFixtureProviderPid, nil)
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferGetHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, transferFixtureProviderPid, ack.ProviderPid)
}

func TestTransferGetHandler_NotOwned(t *testing.T) {
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transfers/"+transferFixtureProviderPid, nil)
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader("someone-else@example.com"))
	rec := httptest.NewRecorder()

	transferGetHandler(rec, req)

	// A non-owner gets the same 404 as an unknown providerPid - see
	// mapTransferServiceError's own comment.
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTransferGetHandler_NotFound(t *testing.T) {
	startFixtureTransferService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transfers/urn:dynamos:transfer:VU:missing", nil)
	req.SetPathValue("providerPid", "urn:dynamos:transfer:VU:missing")
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferGetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTransferStartHandler_StartsFromRequested(t *testing.T) {
	// T3.4 (DSP TCK TP-group): a Consumer may send Start straight from
	// REQUESTED, not only to resume a SUSPENDED transfer - the TCK's own
	// TP_01/TP_02 provider tests do exactly this. The fixture starts in
	// REQUESTED already.
	startFixtureTransferService(t)

	body := `{"consumerPid":"` + transferFixtureConsumerPid + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/start", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferStartHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "STARTED", ack.State)
}

func TestTransferStartHandler_RejectsWhenTerminated(t *testing.T) {
	// TERMINATED is a dead end - Start must still be rejected from there,
	// same as any other invalid source state (DSP TCK TP:03-04).
	fixture := startFixtureTransferService(t)
	*fixture.state = "TERMINATED"

	body := `{"consumerPid":"` + transferFixtureConsumerPid + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/start", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferStartHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-transition", te.Code)
}

func TestTransferStartHandler_ResumesWhenSuspended(t *testing.T) {
	fixture := startFixtureTransferService(t)
	*fixture.state = "SUSPENDED"

	body := `{"consumerPid":"` + transferFixtureConsumerPid + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/start", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferStartHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "STARTED", ack.State)
}

func TestTransferCompletionHandler_Success(t *testing.T) {
	fixture := startFixtureTransferService(t)
	*fixture.state = "STARTED"

	body := `{"consumerPid":"` + transferFixtureConsumerPid + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/completion", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferCompletionHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "COMPLETED", ack.State)
}

func TestTransferSuspensionHandler_Success(t *testing.T) {
	fixture := startFixtureTransferService(t)
	*fixture.state = "STARTED"

	body := `{"consumerPid":"` + transferFixtureConsumerPid + `","code":"99","reason":["network maintenance"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/suspension", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferSuspensionHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "SUSPENDED", ack.State)
}

func TestTransferTerminationHandler_Success(t *testing.T) {
	startFixtureTransferService(t)

	body := `{"consumerPid":"` + transferFixtureConsumerPid + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/termination", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferTerminationHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "TERMINATED", ack.State)
}

func TestTransferTerminationHandler_ConsumerPidMismatch(t *testing.T) {
	startFixtureTransferService(t)

	body := `{"consumerPid":"urn:example:consumer:not-this-one"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/"+transferFixtureProviderPid+"/termination", bytes.NewBufferString(body))
	req.SetPathValue("providerPid", transferFixtureProviderPid)
	req.Header.Set("Authorization", testAuthHeader(transferFixtureParticipant))
	rec := httptest.NewRecorder()

	transferTerminationHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-request", te.Code)
}

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
	transferConsumerFixtureNegotiationId = "urn:dynamos:negotiation:consumer:VU:fixture-1"
	transferConsumerFixtureProviderPid   = "urn:example:negotiation:provider:1"
	transferConsumerFixtureRemote        = "urn:dynamos:party:UVA"
	transferConsumerFixtureEndpoint      = "http://uva.example.com/api/v1"
)

// startFixtureConsumerNegotiationForTransfer stands in for negotiation-service's
// GET /internal/v1/negotiations/consumer/{id}, the lookup
// transferConsumerInitiateHandler makes before it can build a transfer. A
// test picks the negotiation's state/providerEndpoint/providerPid to drive
// each validation branch.
func startFixtureConsumerNegotiationForTransfer(t *testing.T, state, providerEndpoint, providerPid string) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != transferConsumerFixtureNegotiationId {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "negotiation-not-found", "error": "no negotiation found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"providerPid":       providerPid,
			"consumerPid":       transferConsumerFixtureNegotiationId,
			"remoteParticipant": transferConsumerFixtureRemote,
			"providerEndpoint":  providerEndpoint,
			"state":             state,
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL
}

// startFixtureConsumerTransferCollectionService stands in for
// transfer-process-service's POST /internal/v1/transfers/consumer. Records
// the decoded request body so a test can assert what
// transferConsumerInitiateHandler actually sent.
func startFixtureConsumerTransferCollectionService(t *testing.T) (received *map[string]any) {
	t.Helper()

	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/transfers/consumer", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"providerPid": "urn:example:transfer:provider:1",
			"consumerPid": "urn:dynamos:transfer:consumer:VU:initiate-1",
			"state":       "REQUESTED",
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	transferServiceURL = ts.URL
	return &body
}

func transferInitiateBody() string {
	return `{"negotiationId":"` + transferConsumerFixtureNegotiationId + `","format":"dynamos:computeToData"}`
}

func TestTransferConsumerInitiateHandler_Success(t *testing.T) {
	startFixtureConsumerNegotiationForTransfer(t, "FINALIZED", transferConsumerFixtureEndpoint, transferConsumerFixtureProviderPid)
	received := startFixtureConsumerTransferCollectionService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/initiate", bytes.NewBufferString(transferInitiateBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "REQUESTED", ack.State)

	// agreementId must be the negotiation's own providerPid (dsp-connector's
	// established validateAgreementId convention), and remoteParticipant/
	// providerEndpoint must come from the stored negotiation, never from
	// the initiate caller's own DAT.
	assert.Equal(t, transferConsumerFixtureProviderPid, (*received)["agreementId"])
	assert.Equal(t, transferConsumerFixtureRemote, (*received)["remoteParticipant"])
	assert.Equal(t, transferConsumerFixtureEndpoint, (*received)["providerEndpoint"])
	assert.Equal(t, "dynamos:computeToData", (*received)["format"])
}

func TestTransferConsumerInitiateHandler_MissingAuthorization(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/initiate", bytes.NewBufferString(transferInitiateBody()))
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "missing-authorization", te.Code)
}

func TestTransferConsumerInitiateHandler_MissingFormat(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/initiate",
		bytes.NewBufferString(`{"negotiationId":"`+transferConsumerFixtureNegotiationId+`"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-request", te.Code)
}

func TestTransferConsumerInitiateHandler_NegotiationNotFinalized(t *testing.T) {
	startFixtureConsumerNegotiationForTransfer(t, "AGREED", transferConsumerFixtureEndpoint, transferConsumerFixtureProviderPid)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/initiate", bytes.NewBufferString(transferInitiateBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-negotiation", te.Code)
}

func TestTransferConsumerInitiateHandler_UnknownNegotiation(t *testing.T) {
	startFixtureConsumerNegotiationForTransfer(t, "FINALIZED", transferConsumerFixtureEndpoint, transferConsumerFixtureProviderPid)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/initiate",
		bytes.NewBufferString(`{"negotiationId":"urn:dynamos:negotiation:consumer:VU:does-not-exist","format":"dynamos:computeToData"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-negotiation", te.Code)
}

func TestTransferConsumerInitiateHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/transfers/initiate", nil)
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestTransferConsumerInitiateHandler_ProviderRejects(t *testing.T) {
	startFixtureConsumerNegotiationForTransfer(t, "FINALIZED", transferConsumerFixtureEndpoint, transferConsumerFixtureProviderPid)

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/transfers/consumer", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"code": "provider-request-failed", "error": "outbound transfer request to provider failed"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	transferServiceURL = ts.URL

	req := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/initiate", bytes.NewBufferString(transferInitiateBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	transferConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "upstream-error", te.Code)
}

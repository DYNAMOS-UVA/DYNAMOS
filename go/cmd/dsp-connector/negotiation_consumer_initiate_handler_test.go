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

// startFixtureConsumerCollectionService stands in for negotiation-service's
// POST /internal/v1/negotiations/consumer (#80's create-as-consumer),
// the one Consumer-role route startFixtureConsumerNegotiationService doesn't
// cover. Records the decoded request body so a test can assert what
// negotiationConsumerInitiateHandler actually sent.
func startFixtureConsumerCollectionService(t *testing.T) (received *map[string]any) {
	t.Helper()

	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations/consumer", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"providerPid":       "urn:example:provider:initiate-1",
			"consumerPid":       "urn:dynamos:negotiation:consumer:VU:initiate-1",
			"remoteParticipant": body["remoteParticipant"].(string),
			"state":             "REQUESTED",
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL
	return &body
}

func initiateBody() string {
	return `{"providerId":"TCK_PARTICIPANT","offerId":"urn:dynamos:offer:VU:tck-fixture","datasetId":"urn:dynamos:dataset:VU:tck-fixture","connectorAddress":"http://localhost:8083"}`
}

func TestNegotiationConsumerInitiateHandler_Success(t *testing.T) {
	received := startFixtureConsumerCollectionService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/initiate", bytes.NewBufferString(initiateBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	negotiationConsumerInitiateHandler(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var ack negotiationAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "REQUESTED", ack.State)

	// The DAT-verified caller, not body.providerId, must become
	// remoteParticipant - see the handler's own doc comment on why.
	assert.Equal(t, "did:web:localhost%3A9999", (*received)["remoteParticipant"])
	assert.Equal(t, "http://localhost:8083", (*received)["providerEndpoint"])
	offer, ok := (*received)["offer"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "urn:dynamos:offer:VU:tck-fixture", offer["@id"])
}

func TestNegotiationConsumerInitiateHandler_MissingAuthorization(t *testing.T) {
	startFixtureConsumerCollectionService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/initiate", bytes.NewBufferString(initiateBody()))
	rec := httptest.NewRecorder()

	negotiationConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "missing-authorization", ne.Code)
}

func TestNegotiationConsumerInitiateHandler_MissingOfferID(t *testing.T) {
	startFixtureConsumerCollectionService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/initiate",
		bytes.NewBufferString(`{"providerId":"TCK_PARTICIPANT","datasetId":"urn:dynamos:dataset:VU:tck-fixture","connectorAddress":"http://localhost:8083"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	negotiationConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

func TestNegotiationConsumerInitiateHandler_MissingConnectorAddress(t *testing.T) {
	startFixtureConsumerCollectionService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/initiate",
		bytes.NewBufferString(`{"providerId":"TCK_PARTICIPANT","offerId":"urn:dynamos:offer:VU:tck-fixture","datasetId":"urn:dynamos:dataset:VU:tck-fixture"}`))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	negotiationConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "invalid-request", ne.Code)
}

func TestNegotiationConsumerInitiateHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/negotiations/initiate", nil)
	rec := httptest.NewRecorder()

	negotiationConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestNegotiationConsumerInitiateHandler_UpstreamFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/negotiations/consumer", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	negotiationServiceURL = ts.URL

	req := httptest.NewRequest(http.MethodPost, "/api/v1/negotiations/initiate", bytes.NewBufferString(initiateBody()))
	req.Header.Set("Authorization", testAuthHeader("did:web:localhost%3A9999"))
	rec := httptest.NewRecorder()

	negotiationConsumerInitiateHandler(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	var ne negotiationError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ne))
	assert.Equal(t, "upstream-error", ne.Code)
}

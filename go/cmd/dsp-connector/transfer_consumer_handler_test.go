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
	transferCallbackFixtureConsumerPid = "urn:dynamos:transfer:consumer:VU:fixture-1"
	transferCallbackFixtureProviderPid = "urn:example:transfer:provider:fixture-1"
)

// startFixtureConsumerTransferService stands in for transfer-process-service's
// Consumer-role internal API (issue #93), same shape as
// startFixtureConsumerNegotiationService but for the routes
// transfer_consumer_client.go calls.
func startFixtureConsumerTransferService(t *testing.T) (state, remoteParticipant *string) {
	t.Helper()

	s := "REQUESTED"
	rp := "urn:dynamos:party:UVA"
	var dataAddress json.RawMessage

	record := func() map[string]any {
		return map[string]any{
			"providerPid":       transferCallbackFixtureProviderPid,
			"consumerPid":       transferCallbackFixtureConsumerPid,
			"remoteParticipant": rp,
			"state":             s,
			"dataAddress":       dataAddress,
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != transferCallbackFixtureConsumerPid {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "transfer-not-found", "error": "no transfer found for id"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DataAddress json.RawMessage `json:"dataAddress,omitempty"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.DataAddress) > 0 {
			dataAddress = body.DataAddress
		}
		s = "STARTED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/completion", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s != "STARTED" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalid-transition", "error": "state does not allow this transition"})
			return
		}
		s = "COMPLETED"
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/suspension", func(w http.ResponseWriter, r *http.Request) {
		s = "SUSPENDED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/termination", func(w http.ResponseWriter, r *http.Request) {
		s = "TERMINATED"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(record())
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	transferServiceURL = ts.URL
	return &s, &rp
}

func transferConsumerStartBody() string {
	return `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"TransferStartMessage","providerPid":"` + transferCallbackFixtureProviderPid + `","consumerPid":"` + transferCallbackFixtureConsumerPid + `","dataAddress":{"avg_salary_scale_women":"9.400"}}`
}

// TestTransferConsumerGetHandler_Success covers issue #93's status/data
// poll endpoint (GET /:callback/transfers/:consumerPid) - not one of the
// DSP spec's own Consumer Path Bindings, see transferConsumerStatus's own
// doc comment. This is what replaces the echo-listener pod's /latest.
func TestTransferConsumerGetHandler_Success(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid, nil)
	req.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerGetHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var status transferConsumerStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, "REQUESTED", status.State)
}

func TestTransferConsumerGetHandler_WrongParticipant(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid, nil)
	req.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:someone-else"))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerGetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "not-found", te.Code)
}

// TestTransferConsumerStartHandler_DeliversRealData drives the exact demo
// path issue #93 targets: a Provider-initiated push carrying real computed
// data lands on this handler, gets recorded, and is then retrievable via
// GET - end to end, no echo-listener pod involved.
func TestTransferConsumerStartHandler_DeliversRealData(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/start",
		bytes.NewBufferString(transferConsumerStartBody()))
	req.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerStartHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "STARTED", ack.State)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid, nil)
	getReq.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	getReq.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	getRec := httptest.NewRecorder()

	transferConsumerGetHandler(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)
	var status transferConsumerStatus
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &status))
	assert.Equal(t, "STARTED", status.State)
	assert.JSONEq(t, `{"avg_salary_scale_women":"9.400"}`, string(status.DataAddress))
}

func TestTransferConsumerStartHandler_MissingAuthorization(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/start",
		bytes.NewBufferString(transferConsumerStartBody()))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerStartHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "missing-authorization", te.Code)
}

func TestTransferConsumerStartHandler_WrongParticipant(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/start",
		bytes.NewBufferString(transferConsumerStartBody()))
	req.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:someone-else"))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerStartHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "a non-owner must see the same response as a truly unknown consumerPid")
}

func TestTransferConsumerStartHandler_ProviderPidMismatch(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/start",
		bytes.NewBufferString(`{"providerPid":"urn:example:transfer:provider:wrong","consumerPid":"`+transferCallbackFixtureConsumerPid+`"}`))
	req.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerStartHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var te transferError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &te))
	assert.Equal(t, "invalid-request", te.Code)
}

// TestTransferConsumerLifecycle_StartCompletion drives one Consumer-role
// transfer through STARTED -> COMPLETED via the real HTTP handlers.
func TestTransferConsumerLifecycle_StartCompletion(t *testing.T) {
	startFixtureConsumerTransferService(t)

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/start",
		bytes.NewBufferString(transferConsumerStartBody()))
	startReq.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	startReq.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	startRec := httptest.NewRecorder()
	transferConsumerStartHandler(startRec, startReq)
	require.Equal(t, http.StatusOK, startRec.Code)

	completeReq := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/completion",
		bytes.NewBufferString(`{"providerPid":"`+transferCallbackFixtureProviderPid+`","consumerPid":"`+transferCallbackFixtureConsumerPid+`"}`))
	completeReq.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	completeReq.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	completeRec := httptest.NewRecorder()
	transferConsumerCompletionHandler(completeRec, completeReq)

	require.Equal(t, http.StatusOK, completeRec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(completeRec.Body.Bytes(), &ack))
	assert.Equal(t, "COMPLETED", ack.State)
}

func TestTransferConsumerTerminationHandler_LogsAndRecords(t *testing.T) {
	startFixtureConsumerTransferService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid+"/termination",
		bytes.NewBufferString(`{"providerPid":"`+transferCallbackFixtureProviderPid+`","consumerPid":"`+transferCallbackFixtureConsumerPid+`","code":"job-failed","reason":["upstream job pipeline error"]}`))
	req.Header.Set("Authorization", testAuthHeader("urn:dynamos:party:UVA"))
	req.SetPathValue("consumerPid", transferCallbackFixtureConsumerPid)
	rec := httptest.NewRecorder()

	transferConsumerTerminationHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var ack transferAck
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	assert.Equal(t, "TERMINATED", ack.State)
}

func TestTransferConsumerGetHandler_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/transfers/"+transferCallbackFixtureConsumerPid, nil)
	rec := httptest.NewRecorder()

	transferConsumerGetHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFixtureAPIGateway stands in for api-gateway's own
// /api/v1/requestApproval endpoint. handler decides the response, so
// each test can pick success, failure, or a malformed body.
func startFixtureAPIGateway(t *testing.T, handler http.HandlerFunc) {
	t.Helper()

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	apiGatewayURL = ts.URL
	apiGatewayHost = ""
}

func fixtureTransferForJob(dataAddress json.RawMessage) *TransferProcess {
	return newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "dynamos:sqlDataRequest", "https://consumer.example.com/callback", dataAddress)
}

// TestRequestJobExecution_Success checks two things. First, the job
// spec (dataAddress) and the request type (format, "dynamos:" stripped)
// reach api-gateway inside an api.RequestApproval body. Second, a 2xx
// JSON reply comes back as the job result.
func TestRequestJobExecution_Success(t *testing.T) {
	var captured map[string]any
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jobId":"job-1","responses":["ok"]}`))
	})

	tp := fixtureTransferForJob(json.RawMessage(`{"query":"SELECT 1"}`))
	result, err := requestJobExecution(tp)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jobId":"job-1","responses":["ok"]}`, string(result))

	assert.Equal(t, "sqlDataRequest", captured["type"])
	assert.Equal(t, []any{"VU"}, captured["dataProviders"])
	assert.Equal(t, map[string]any{"query": "SELECT 1"}, captured["data_request"])
}

// TestRequestJobExecution_RejectsEmptyDataAddress pins the guard
// against sending api-gateway a present-but-null data_request. That
// field has no omitempty tag (pkg/api.RequestApproval), so a nil
// DataAddress would otherwise marshal to "data_request": null.
// api-gateway's own requestHandler unmarshals null into a nil map, then
// panics on the first write into it.
func TestRequestJobExecution_RejectsEmptyDataAddress(t *testing.T) {
	tp := fixtureTransferForJob(nil)
	_, err := requestJobExecution(tp)
	assert.Error(t, err)
}

// TestRequestJobExecution_RejectsErrorStatus confirms a non-2xx reply
// becomes a Go error, not a result. api-gateway itself returns one, for
// example a 408 on a policy-enforcer/orchestrator timeout.
func TestRequestJobExecution_RejectsErrorStatus(t *testing.T) {
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Request timed out", http.StatusRequestTimeout)
	})

	tp := fixtureTransferForJob(json.RawMessage(`{"query":"SELECT 1"}`))
	_, err := requestJobExecution(tp)
	assert.Error(t, err)
}

// TestRequestJobExecution_RejectsNonJSONBody guards against a 2xx
// reply with an empty or non-JSON body. api-gateway's own requestHandler
// has early-return paths that write no body at all on some internal
// errors, defaulting to a 200 with nothing in it.
func TestRequestJobExecution_RejectsNonJSONBody(t *testing.T) {
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tp := fixtureTransferForJob(json.RawMessage(`{"query":"SELECT 1"}`))
	_, err := requestJobExecution(tp)
	assert.Error(t, err)
}

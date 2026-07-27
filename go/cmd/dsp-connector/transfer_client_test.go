package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startCapturingTransferService stands in for transfer-process-service. It
// hands back the raw request body of the next call. A test can then check
// the exact JSON dsp-connector sent - not just the decoded Go value, which
// would hide a present-but-null field.
func startCapturingTransferService(t *testing.T) *[]byte {
	t.Helper()

	var captured []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"providerPid": transferFixtureProviderPid,
			"consumerPid": transferFixtureConsumerPid,
			"participant": transferFixtureParticipant,
			"state":       "REQUESTED",
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	transferServiceURL = ts.URL
	return &captured
}

// TestCreateTransfer_OmitsDataAddressWhenEmpty pins the fix for a real bug.
// A present "dataAddress" key with a nil json.RawMessage value marshals to
// the JSON literal null, not to a missing field. transfer-process-service's
// own /internal/v1/transfers handler only skips an empty dataAddress when
// the key is missing. It never checks for a null value on this endpoint.
// A present null would have been stored as the transfer's DataAddress.
func TestCreateTransfer_OmitsDataAddressWhenEmpty(t *testing.T) {
	captured := startCapturingTransferService(t)

	_, err := createTransfer(transferFixtureConsumerPid, transferFixtureParticipant, transferFixtureAgreementId, "example:HTTP_PULL", transferFixtureCallbackAddr, nil)
	require.NoError(t, err)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(*captured, &body))
	_, present := body["dataAddress"]
	assert.False(t, present, "dataAddress key must be absent, not present-and-null, when no dataAddress was given")
}

// TestStartTransfer_OmitsDataAddressWhenEmpty is startTransfer's own
// version of TestCreateTransfer_OmitsDataAddressWhenEmpty. This one matters
// more in practice: it fires on every ordinary resume-after-suspend call,
// and those almost never carry a new dataAddress.
func TestStartTransfer_OmitsDataAddressWhenEmpty(t *testing.T) {
	captured := startCapturingTransferService(t)

	_, err := startTransfer(transferFixtureProviderPid, nil)
	require.NoError(t, err)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(*captured, &body))
	_, present := body["dataAddress"]
	assert.False(t, present, "dataAddress key must be absent, not present-and-null, when no dataAddress was given")
}

// TestCreateTransfer_IncludesDataAddressWhenPresent confirms the fix did
// not overcorrect: a real dataAddress must still round-trip.
func TestCreateTransfer_IncludesDataAddressWhenPresent(t *testing.T) {
	captured := startCapturingTransferService(t)

	_, err := createTransfer(transferFixtureConsumerPid, transferFixtureParticipant, transferFixtureAgreementId, "example:HTTP_PULL", transferFixtureCallbackAddr, []byte(`{"endpoint":"http://example.com"}`))
	require.NoError(t, err)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(*captured, &body))
	require.Contains(t, body, "dataAddress")
	assert.JSONEq(t, `{"endpoint":"http://example.com"}`, string(body["dataAddress"]))
}

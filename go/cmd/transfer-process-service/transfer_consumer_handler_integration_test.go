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

// wireTransferHandlerTestStore wires the package-level etcdClient/party/store
// (normally set in main()) against a real etcd, same convention as
// negotiation-service's own wireHandlerTestStore.
func wireTransferHandlerTestStore(t *testing.T) {
	t.Helper()

	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	etcdClient = etcd.GetEtcdClient(endpoint)
	party = "VU"
	store = NewStore(etcdClient)
}

func doTransferRequest(h http.HandlerFunc, method, target, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeTransfer(t *testing.T, rec *httptest.ResponseRecorder) *TransferProcess {
	t.Helper()
	var tp TransferProcess
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tp))
	return &tp
}

func decodeTransferInternalError(t *testing.T, rec *httptest.ResponseRecorder) internalError {
	t.Helper()
	var ie internalError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
	return ie
}

// fakeTransferProvider stands in for the remote dsp-connector's
// POST /transfers/request endpoint - just enough of the DSP ack shape
// (providerPid) for sendTransferRequest to adopt the transfer, real HTTP
// round-trip included (not a mock of sendTransferRequest itself). Mirrors
// negotiation-service's fakeProvider.
func fakeTransferProvider(t *testing.T, providerPid string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/transfers/request", r.URL.Path)
		assert.Equal(t, "urn:dynamos:party:VU", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"@context":    dspContext,
			"@type":       "TransferProcess",
			"providerPid": providerPid,
			"consumerPid": "",
			"state":       "REQUESTED",
		})
	}))
}

func TestTransfersConsumerCollectionHandler_Create(t *testing.T) {
	wireTransferHandlerTestStore(t)

	provider := fakeTransferProvider(t, "urn:example:transfer:provider:1")
	defer provider.Close()

	rec := doTransferRequest(transfersConsumerCollectionHandler, http.MethodPost, "/internal/v1/transfers/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"urn:dynamos:party:VU","remoteParticipant":"urn:dynamos:party:UVA","agreementId":"urn:example:agreement:1","format":"dynamos:computeToData","callbackAddress":"https://vu.example.com/api/v1/callback"}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	tp := decodeTransfer(t, rec)
	assert.Equal(t, KindConsumer, tp.Kind)
	assert.Equal(t, StateRequested, tp.State)
	assert.Equal(t, "urn:example:transfer:provider:1", tp.ProviderPid)
	assert.Equal(t, "urn:dynamos:party:UVA", tp.RemoteParticipant)
	assert.Contains(t, tp.ConsumerPid, "urn:dynamos:transfer:consumer:VU:")
}

func TestTransfersConsumerCollectionHandler_MissingProviderEndpoint(t *testing.T) {
	wireTransferHandlerTestStore(t)

	rec := doTransferRequest(transfersConsumerCollectionHandler, http.MethodPost, "/internal/v1/transfers/consumer", "",
		`{"participant":"urn:dynamos:party:VU","remoteParticipant":"urn:dynamos:party:UVA","agreementId":"urn:example:agreement:1","format":"dynamos:computeToData","callbackAddress":"https://vu.example.com/api/v1/callback"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-provider-endpoint", decodeTransferInternalError(t, rec).Code)
}

func TestTransfersConsumerCollectionHandler_MissingAgreementId(t *testing.T) {
	wireTransferHandlerTestStore(t)

	provider := fakeTransferProvider(t, "urn:example:transfer:provider:missing-agreement")
	defer provider.Close()

	rec := doTransferRequest(transfersConsumerCollectionHandler, http.MethodPost, "/internal/v1/transfers/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"urn:dynamos:party:VU","remoteParticipant":"urn:dynamos:party:UVA","format":"dynamos:computeToData","callbackAddress":"https://vu.example.com/api/v1/callback"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing-agreement-id", decodeTransferInternalError(t, rec).Code)
}

// TestTransfersConsumerCollectionHandler_ProviderRejects confirms a
// non-2xx from the remote Provider fails the internal-API call and
// persists nothing - there must be no local record of a transfer the
// remote side never actually accepted.
func TestTransfersConsumerCollectionHandler_ProviderRejects(t *testing.T) {
	wireTransferHandlerTestStore(t)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer provider.Close()

	rec := doTransferRequest(transfersConsumerCollectionHandler, http.MethodPost, "/internal/v1/transfers/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"urn:dynamos:party:VU","remoteParticipant":"urn:dynamos:party:UVA","agreementId":"urn:example:agreement:1","format":"dynamos:computeToData","callbackAddress":"https://vu.example.com/api/v1/callback"}`)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "provider-request-failed", decodeTransferInternalError(t, rec).Code)
}

// TestTransferConsumerLifecycle_FullPath drives one Consumer-role transfer
// through every transition end to end: REQUESTED -> STARTED (Provider push)
// -> COMPLETED, then confirms TERMINATED is correctly rejected as a
// dead-end move. No auto-decision policy is involved anywhere in this
// path - every inbound message here is just recorded, unlike negotiation's
// equivalent lifecycle test which drives auto-accept/auto-verify.
func TestTransferConsumerLifecycle_FullPath(t *testing.T) {
	wireTransferHandlerTestStore(t)

	provider := fakeTransferProvider(t, "urn:example:transfer:provider:lifecycle")
	defer provider.Close()

	createRec := doTransferRequest(transfersConsumerCollectionHandler, http.MethodPost, "/internal/v1/transfers/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"urn:dynamos:party:VU","remoteParticipant":"urn:dynamos:party:UVA","agreementId":"urn:example:agreement:1","format":"dynamos:computeToData","callbackAddress":"https://vu.example.com/api/v1/callback"}`)
	require.Equal(t, http.StatusCreated, createRec.Code)
	id := decodeTransfer(t, createRec).ConsumerPid

	startRec := doTransferRequest(transferConsumerStartHandler, http.MethodPost, "/internal/v1/transfers/consumer/"+id+"/start", id,
		`{"dataAddress":{"endpoint":"http://example.com"}}`)
	require.Equal(t, http.StatusOK, startRec.Code)
	started := decodeTransfer(t, startRec)
	assert.Equal(t, StateStarted, started.State)
	assert.JSONEq(t, `{"endpoint":"http://example.com"}`, string(started.DataAddress))

	completeRec := doTransferRequest(transferConsumerCompletionHandler, http.MethodPost, "/internal/v1/transfers/consumer/"+id+"/completion", id, `{}`)
	require.Equal(t, http.StatusOK, completeRec.Code)
	assert.Equal(t, StateCompleted, decodeTransfer(t, completeRec).State)

	terminateRec := doTransferRequest(transferConsumerTerminationHandler, http.MethodPost, "/internal/v1/transfers/consumer/"+id+"/termination", id, `{}`)
	assert.Equal(t, http.StatusConflict, terminateRec.Code)
	assert.Equal(t, "invalid-transition", decodeTransferInternalError(t, terminateRec).Code)
}

// TestTransferConsumerAndProvider_DoNotCollide is the handler-level
// counterpart to the store-level namespace split - a Provider-role and a
// Consumer-role transfer created independently through the real HTTP
// handlers must never be visible to each other's GET endpoint.
func TestTransferConsumerAndProvider_DoNotCollide(t *testing.T) {
	wireTransferHandlerTestStore(t)

	provider := fakeTransferProvider(t, "urn:example:transfer:provider:no-collide")
	defer provider.Close()

	providerRoleRec := doTransferRequest(transfersCollectionHandler, http.MethodPost, "/internal/v1/transfers", "",
		`{"consumerPid":"urn:example:consumer:no-collide","participant":"consumer@example.com","agreementId":"urn:example:agreement:1","format":"dynamos:computeToData","callbackAddress":"https://consumer.example.com/callback"}`)
	require.Equal(t, http.StatusCreated, providerRoleRec.Code)
	providerRoleId := decodeTransfer(t, providerRoleRec).ProviderPid

	consumerRoleRec := doTransferRequest(transfersConsumerCollectionHandler, http.MethodPost, "/internal/v1/transfers/consumer", "",
		`{"providerEndpoint":"`+provider.URL+`","participant":"urn:dynamos:party:VU","remoteParticipant":"urn:dynamos:party:UVA","agreementId":"urn:example:agreement:1","format":"dynamos:computeToData","callbackAddress":"https://vu.example.com/api/v1/callback"}`)
	require.Equal(t, http.StatusCreated, consumerRoleRec.Code)
	consumerRoleId := decodeTransfer(t, consumerRoleRec).ConsumerPid

	notFoundRec := doTransferRequest(transferConsumerHandler, http.MethodGet, "/internal/v1/transfers/consumer/"+providerRoleId, providerRoleId, "")
	assert.Equal(t, http.StatusNotFound, notFoundRec.Code, "the Provider-role transfer must not be reachable through the Consumer-role GET")

	notFoundRec2 := doTransferRequest(transferHandler, http.MethodGet, "/internal/v1/transfers/"+consumerRoleId, consumerRoleId, "")
	assert.Equal(t, http.StatusNotFound, notFoundRec2.Code, "the Consumer-role transfer must not be reachable through the Provider-role GET")
}

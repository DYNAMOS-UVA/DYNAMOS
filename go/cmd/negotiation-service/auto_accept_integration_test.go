//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProviderLifecycle stands in for a remote Provider across #82's full
// outbound sequence: initiating request, then the autonomous ACCEPTED
// event, then the autonomous Agreement Verification. Tracks hit counts and
// can be told to reject either autonomous call, so a test can assert both
// that the call fired and how the negotiation behaves if the Provider
// refuses it.
type fakeProviderLifecycle struct {
	eventsHits         atomic.Int32
	verificationHits   atomic.Int32
	rejectEvents       atomic.Bool
	rejectVerification atomic.Bool
}

func startFakeProviderLifecycle(t *testing.T, providerPid string) (*httptest.Server, *fakeProviderLifecycle) {
	t.Helper()
	f := &fakeProviderLifecycle{}

	mux := http.NewServeMux()
	mux.HandleFunc("/negotiations/request", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"providerPid": providerPid, "state": "REQUESTED"})
	})
	mux.HandleFunc("/negotiations/"+providerPid+"/events", func(w http.ResponseWriter, r *http.Request) {
		f.eventsHits.Add(1)
		if f.rejectEvents.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/negotiations/"+providerPid+"/agreement/verification", func(w http.ResponseWriter, r *http.Request) {
		f.verificationHits.Add(1)
		if f.rejectVerification.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, f
}

// createConsumerNegotiation drives negotiationsConsumerCollectionHandler to
// stand up a fresh Consumer-role negotiation against provider, returning its
// ConsumerPid.
func createConsumerNegotiation(t *testing.T, providerURL string) string {
	t.Helper()
	rec := doRequest(negotiationsConsumerCollectionHandler, http.MethodPost, "/internal/v1/negotiations/consumer", "",
		`{"providerEndpoint":"`+providerURL+`","participant":"did:web:vu.example.com","remoteParticipant":"did:web:surf.example.com","callbackAddress":"https://vu.example.com/callback","offer":{"@id":"offer-1"}}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	return decodeNegotiation(t, rec).ConsumerPid
}

// TestAutoAcceptOffer_Success is #82's core acceptance test: an inbound
// Offer must autonomously drive the negotiation all the way to ACCEPTED,
// with a real outbound ACCEPTED event actually delivered to the Provider.
func TestAutoAcceptOffer_Success(t *testing.T) {
	wireHandlerTestStore(t)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:auto-accept")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	offerRec := doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/offer", consumerPid,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)

	require.Equal(t, http.StatusOK, offerRec.Code)
	assert.Equal(t, StateAccepted, decodeNegotiation(t, offerRec).State)
	assert.Equal(t, int32(1), fixture.eventsHits.Load(), "the outbound ACCEPTED event must actually be sent")
}

// TestAutoAcceptOffer_ProviderRejects confirms a Provider refusing the
// autonomous ACCEPTED event leaves the negotiation at OFFERED rather than
// silently claiming ACCEPTED - the offer recording itself must still
// succeed (200), matching autoAcceptOffer's documented failure behavior.
func TestAutoAcceptOffer_ProviderRejects(t *testing.T) {
	wireHandlerTestStore(t)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:auto-accept-reject")
	fixture.rejectEvents.Store(true)
	consumerPid := createConsumerNegotiation(t, provider.URL)

	offerRec := doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/offer", consumerPid,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)

	require.Equal(t, http.StatusOK, offerRec.Code)
	assert.Equal(t, StateOffered, decodeNegotiation(t, offerRec).State, "a rejected auto-accept must leave the negotiation at OFFERED, not silently ACCEPTED")
	assert.Equal(t, int32(1), fixture.eventsHits.Load())
}

// TestAutoVerifyAgreement_Success is #82's second half: an inbound
// Agreement must autonomously drive the negotiation to VERIFIED, with a
// real outbound Agreement Verification Message delivered to the Provider.
func TestAutoVerifyAgreement_Success(t *testing.T) {
	wireHandlerTestStore(t)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:auto-verify")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	agreementRec := doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/agreement", consumerPid,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)

	require.Equal(t, http.StatusOK, agreementRec.Code)
	assert.Equal(t, StateVerified, decodeNegotiation(t, agreementRec).State)
	assert.Equal(t, int32(1), fixture.verificationHits.Load(), "the outbound Agreement Verification Message must actually be sent")
}

// TestAutoVerifyAgreement_ProviderRejects mirrors
// TestAutoAcceptOffer_ProviderRejects for the AGREED -> VERIFIED step.
func TestAutoVerifyAgreement_ProviderRejects(t *testing.T) {
	wireHandlerTestStore(t)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:auto-verify-reject")
	fixture.rejectVerification.Store(true)
	consumerPid := createConsumerNegotiation(t, provider.URL)

	agreementRec := doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/agreement", consumerPid,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)

	require.Equal(t, http.StatusOK, agreementRec.Code)
	assert.Equal(t, StateAgreed, decodeNegotiation(t, agreementRec).State, "a rejected auto-verify must leave the negotiation at AGREED, not silently VERIFIED")
	assert.Equal(t, int32(1), fixture.verificationHits.Load())
}

// TestAutoAcceptThenAutoVerify_FullPath drives one negotiation through the
// entire autonomous sequence end to end: Offer -> auto ACCEPTED -> Agreement
// (valid from ACCEPTED) -> auto VERIFIED, confirming the two autonomous
// steps compose correctly within a single negotiation's lifecycle.
func TestAutoAcceptThenAutoVerify_FullPath(t *testing.T) {
	wireHandlerTestStore(t)

	provider, fixture := startFakeProviderLifecycle(t, "urn:example:provider:auto-full-path")
	consumerPid := createConsumerNegotiation(t, provider.URL)

	offerRec := doRequest(negotiationConsumerOfferHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/offer", consumerPid,
		`{"offer":{"@id":"offer-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, offerRec.Code)
	require.Equal(t, StateAccepted, decodeNegotiation(t, offerRec).State)

	agreementRec := doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/agreement", consumerPid,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, agreementRec.Code)
	assert.Equal(t, StateVerified, decodeNegotiation(t, agreementRec).State)

	assert.Equal(t, int32(1), fixture.eventsHits.Load())
	assert.Equal(t, int32(1), fixture.verificationHits.Load())
}

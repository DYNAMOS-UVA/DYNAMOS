//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForHits polls counter until it reaches want or the deadline passes -
// same reasoning as waitForConsumerState, for asserting the async goroutine
// actually attempted its outbound send before checking the (unchanged) state
// that follows a rejection.
func waitForHits(t *testing.T, counter *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if counter.Load() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hit counter did not reach %d in time, currently %d", want, counter.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForConsumerState polls store directly (in-process, no HTTP) until
// consumerPid reaches want or the deadline passes - autoAcceptOffer/
// autoVerifyAgreement run in a goroutine (#83: a synchronous outbound call
// back to the Provider that just sent the triggering message raced its own
// embedded server in a live TCK run, see auto_accept.go's doc), so their
// effect is no longer visible in the triggering request's own response.
func waitForConsumerState(t *testing.T, consumerPid string, want State) *Negotiation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := store.Get(KindConsumer, consumerPid)
		if err == nil && n.State == want {
			return n
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("negotiation %s did not reach state %s in time: %v", consumerPid, want, err)
			}
			t.Fatalf("negotiation %s did not reach state %s in time, currently %s", consumerPid, want, n.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

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
	assert.Equal(t, StateOffered, decodeNegotiation(t, offerRec).State, "the ack itself always reports OFFERED - auto-accept runs after this response, in the background")
	waitForConsumerState(t, consumerPid, StateAccepted)
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
	waitForHits(t, &fixture.eventsHits, 1)
	n, err := store.Get(KindConsumer, consumerPid)
	require.NoError(t, err)
	assert.Equal(t, StateOffered, n.State, "a rejected auto-accept must leave the negotiation at OFFERED, not silently ACCEPTED")
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
	assert.Equal(t, StateAgreed, decodeNegotiation(t, agreementRec).State, "the ack itself always reports AGREED - auto-verify runs after this response, in the background")
	waitForConsumerState(t, consumerPid, StateVerified)
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
	waitForHits(t, &fixture.verificationHits, 1)
	n, err := store.Get(KindConsumer, consumerPid)
	require.NoError(t, err)
	assert.Equal(t, StateAgreed, n.State, "a rejected auto-verify must leave the negotiation at AGREED, not silently VERIFIED")
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
	require.Equal(t, StateOffered, decodeNegotiation(t, offerRec).State)
	// Must actually reach ACCEPTED before sending the Agreement below -
	// unlike the old synchronous version, the auto-accept goroutine racing
	// this test's own next request is now a real possibility, and AGREED is
	// not a valid transition from OFFERED (negotiationConsumerAgreementHandler
	// only allows ACCEPTED or REQUESTED).
	waitForConsumerState(t, consumerPid, StateAccepted)

	agreementRec := doRequest(negotiationConsumerAgreementHandler, http.MethodPost, "/internal/v1/negotiations/consumer/"+consumerPid+"/agreement", consumerPid,
		`{"agreement":{"@id":"agr-1","target":"ds-1"}}`)
	require.Equal(t, http.StatusOK, agreementRec.Code)
	require.Equal(t, StateAgreed, decodeNegotiation(t, agreementRec).State)
	waitForConsumerState(t, consumerPid, StateVerified)

	assert.Equal(t, int32(1), fixture.eventsHits.Load())
	assert.Equal(t, int32(1), fixture.verificationHits.Load())
}

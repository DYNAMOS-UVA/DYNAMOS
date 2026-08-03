package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// sendAcceptedEvent POSTs a Contract Negotiation Event Message (eventType
// ACCEPTED) to n's remote Provider, synchronously - same single-attempt
// shape as sendContractRequest (this determines DYNAMOS's own next
// transition, unlike deliverToConsumer's fire-and-forget push, which only
// notifies a side that already knows the state changed).
func sendAcceptedEvent(n *Negotiation) error {
	body, err := json.Marshal(map[string]any{
		"@context":    dspContext,
		"@type":       "ContractNegotiationEventMessage",
		"providerPid": n.ProviderPid,
		"consumerPid": n.ConsumerPid,
		"eventType":   "ACCEPTED",
	})
	if err != nil {
		return fmt.Errorf("%w: marshaling request: %w", ErrProviderRequestFailed, err)
	}
	return postToProvider(n, "/negotiations/"+n.ProviderPid+"/events", body)
}

// sendAgreementVerification POSTs a Contract Agreement Verification Message
// to n's remote Provider, synchronously - same shape as sendAcceptedEvent.
func sendAgreementVerification(n *Negotiation) error {
	body, err := json.Marshal(map[string]any{
		"@context":    dspContext,
		"@type":       "ContractAgreementVerificationMessage",
		"providerPid": n.ProviderPid,
		"consumerPid": n.ConsumerPid,
	})
	if err != nil {
		return fmt.Errorf("%w: marshaling request: %w", ErrProviderRequestFailed, err)
	}
	return postToProvider(n, "/negotiations/"+n.ProviderPid+"/agreement/verification", body)
}

// postToProvider POSTs body to n.ProviderEndpoint+path, authenticated as
// n.Participant (DYNAMOS's own identity) - shared by sendAcceptedEvent and
// sendAgreementVerification, which only differ in path and message shape.
// A non-2xx response is the only failure mode that matters here: unlike
// sendContractRequest, the caller doesn't need anything out of the response
// body, just to know whether the Provider accepted the message.
func postToProvider(n *Negotiation, path string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, n.ProviderEndpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: building request: %w", ErrProviderRequestFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", n.Participant)

	client := http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProviderRequestFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: provider returned status %d", ErrProviderRequestFailed, resp.StatusCode)
	}
	return nil
}

// autoAcceptOffer is #82's trivial accept-all decision policy: given n
// already persisted in OFFERED, always accept it. No real evaluation yet -
// per the 2026-08-03 standup decision, agents don't participate in DSP
// policy enforcement at all today, so there is nothing to evaluate against;
// this is deliberately the simplest thing that lets a real negotiation
// finish end to end. A future consortium-agreement-ID check (standup
// decision 3) is what turns this into a real accept/reject - not built yet,
// not blocking.
//
// On success, transitions n to ACCEPTED and persists it, returning the
// updated record. On failure (network error, Provider rejects the event),
// n is returned unchanged except for the OFFERED save that already
// happened before this was called - the failure is logged, not retried:
// there is no queue or retry mechanism in this first version, matching the
// issue's "infrastructure first" framing. A stuck OFFERED negotiation is
// visible via GET and can be retried by a future re-delivery of the same
// Offer (transitionOrError already allows OFFERED -> OFFERED).
func autoAcceptOffer(n *Negotiation) *Negotiation {
	if err := sendAcceptedEvent(n); err != nil {
		logger.Sugar().Errorw("auto-accept: outbound ACCEPTED event failed, negotiation stays OFFERED", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		return n
	}

	accepted := n.clone()
	if err := accepted.transition(StateAccepted, StateOffered); err != nil {
		// Only possible if n's own state changed between the OFFERED save
		// and here, within the same request - not reachable today (nothing
		// else touches this negotiation concurrently), but fail closed
		// rather than silently skip the transition.
		logger.Sugar().Errorw("auto-accept: local transition to ACCEPTED rejected after a successful outbound send", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		return n
	}
	if err := store.Save(accepted); err != nil {
		logger.Sugar().Errorw("auto-accept: failed to persist ACCEPTED", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		return n
	}
	return accepted
}

// autoVerifyAgreement is #82's trivial accept-all policy's second half:
// given n already persisted in AGREED, always verify it. Same
// success/failure shape as autoAcceptOffer.
func autoVerifyAgreement(n *Negotiation) *Negotiation {
	if err := sendAgreementVerification(n); err != nil {
		logger.Sugar().Errorw("auto-verify: outbound Agreement Verification failed, negotiation stays AGREED", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		return n
	}

	verified := n.clone()
	if err := verified.transition(StateVerified, StateAgreed); err != nil {
		logger.Sugar().Errorw("auto-verify: local transition to VERIFIED rejected after a successful outbound send", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		return n
	}
	if err := store.Save(verified); err != nil {
		logger.Sugar().Errorw("auto-verify: failed to persist VERIFIED", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		return n
	}
	return verified
}

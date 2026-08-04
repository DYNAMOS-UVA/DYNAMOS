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

// sendCounterRequest POSTs a counter Contract Request Message (providerPid
// set, per contract-request-message-schema.json's oneOf{callbackAddress,
// providerPid}) to n's remote Provider - #83's explicit counter-offer
// action, only ever called when consumerAutoNegotiate is off (see main.go's
// doc comment): accept-all never counters anything, it has no basis to
// propose different terms.
func sendCounterRequest(n *Negotiation, offer json.RawMessage) error {
	body, err := json.Marshal(map[string]any{
		"@context":    dspContext,
		"@type":       "ContractRequestMessage",
		"providerPid": n.ProviderPid,
		"consumerPid": n.ConsumerPid,
		"offer":       offer,
	})
	if err != nil {
		return fmt.Errorf("%w: marshaling request: %w", ErrProviderRequestFailed, err)
	}
	return postToProvider(n, "/negotiations/"+n.ProviderPid+"/request", body)
}

// sendConsumerTermination POSTs a Contract Negotiation Termination Message
// to n's remote Provider - #83's explicit outbound-termination action, a
// capability DYNAMOS never had before (deliverToConsumer only pushes
// Provider-initiated messages the other way). Only meaningful when
// consumerAutoNegotiate is off - accept-all never decides to terminate.
func sendConsumerTermination(n *Negotiation) error {
	body, err := json.Marshal(map[string]any{
		"@context":    dspContext,
		"@type":       "ContractNegotiationTerminationMessage",
		"providerPid": n.ProviderPid,
		"consumerPid": n.ConsumerPid,
	})
	if err != nil {
		return fmt.Errorf("%w: marshaling request: %w", ErrProviderRequestFailed, err)
	}
	return postToProvider(n, "/negotiations/"+n.ProviderPid+"/termination", body)
}

// postToProvider POSTs body to n.ProviderEndpoint+path, authenticated as
// n.Participant (DYNAMOS's own identity) - shared by sendAcceptedEvent and
// sendAgreementVerification, which only differ in path and message shape.
// A non-2xx response is the only failure mode that matters here: unlike
// sendContractRequest, the caller doesn't need anything out of the response
// body, just to know whether the Provider accepted the message.
//
// Short retry (5 attempts, 250ms exponential backoff) - same shape and same
// real reason as deliverToConsumer's own retry (outbound_delivery.go): a
// caller reacting to a state change via a fast side channel (an etcd
// watch - #83's tck_auto_responder_consumer.go is exactly that) can fire
// before the counterparty's own handler for this specific path finishes
// registering, producing a transient 404 that isn't a real rejection.
// Confirmed live (#83): CN_C:02-02's outbound termination hit exactly this
// race - our watcher reacted to the negotiation reaching REQUESTED and
// POSTed before the TCK's own expectTerminationMessage stage had even run
// yet to register its handler.
func postToProvider(n *Negotiation, path string, body []byte) error {
	client := http.Client{Timeout: 20 * time.Second}

	const maxAttempts = 5
	backoff := 250 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, n.ProviderEndpoint+path, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("%w: building request: %w", ErrProviderRequestFailed, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", n.Participant)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: %w", ErrProviderRequestFailed, err)
		} else {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("%w: provider returned status %d", ErrProviderRequestFailed, resp.StatusCode)
		}

		if attempt < maxAttempts {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return lastErr
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
// A no-op if consumerAutoNegotiate is false (#83, see main.go's doc
// comment) - n stays at OFFERED for something else to explicitly drive
// (negotiationConsumerAcceptHandler, or the future real decision policy
// that flag exists for).
//
// Otherwise runs asynchronously (goroutine), not inline in the caller's own
// request - originally this was synchronous, returning the updated
// ACCEPTED record straight to the caller. Confirmed live against the real
// DSP TCK (#83): a real Provider's inbound Offer POST handler can be
// blocked waiting on its own HTTP response while DYNAMOS is still inside
// that same request, so a synchronous outbound call back to that Provider -
// even to a different path - raced its own embedded server and failed with
// a bare EOF (connection closed with no response) on every live run. The
// DSP HTTPS binding doesn't require it either: a Contract Offer Message's
// ack carries no state (the TCK's own contractOffer client never even
// reads the response body), so nothing downstream actually depends on this
// finishing before the inbound POST returns - same reasoning
// deliverToConsumer already documented for its own async pushes.
func autoAcceptOffer(n *Negotiation) {
	if !consumerAutoNegotiate {
		logger.Sugar().Infow("consumerAutoNegotiate is false, leaving negotiation at OFFERED for explicit driving", "consumerPid", n.ConsumerPid)
		return
	}
	go func() {
		if _, err := acceptOffer(n); err != nil {
			logger.Sugar().Errorw("auto-accept: failed, negotiation stays OFFERED", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		}
	}()
}

// acceptOffer is autoAcceptOffer's synchronous core, shared with the
// explicit negotiationConsumerAcceptHandler (#83): sends the outbound
// ACCEPTED event, then transitions n to ACCEPTED and persists it. On
// failure (network error, Provider rejects the event), n stays at OFFERED -
// not retried, no queue/retry mechanism in this first version, matching the
// issue's "infrastructure first" framing. A stuck OFFERED negotiation is
// visible via GET and can be retried by a future re-delivery of the same
// Offer (transitionOrError already allows OFFERED -> OFFERED).
func acceptOffer(n *Negotiation) (*Negotiation, error) {
	if err := sendAcceptedEvent(n); err != nil {
		return n, err
	}

	accepted := n.clone()
	if err := accepted.transition(StateAccepted, StateOffered); err != nil {
		// Only possible if n's own state changed between the OFFERED save
		// and here - fail closed rather than silently skip the transition.
		return n, err
	}
	if err := store.Save(accepted); err != nil {
		return n, err
	}
	return accepted, nil
}

// autoVerifyAgreement is #82's trivial accept-all policy's second half:
// given n already persisted in AGREED, always verify it. Same
// consumerAutoNegotiate gate and async reasoning as autoAcceptOffer - the
// DSP TCK's own contractAgreement client likewise never reads this
// endpoint's response body.
func autoVerifyAgreement(n *Negotiation) {
	if !consumerAutoNegotiate {
		logger.Sugar().Infow("consumerAutoNegotiate is false, leaving negotiation at AGREED for explicit driving", "consumerPid", n.ConsumerPid)
		return
	}
	go func() {
		if _, err := verifyAgreement(n); err != nil {
			logger.Sugar().Errorw("auto-verify: failed, negotiation stays AGREED", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		}
	}()
}

// verifyAgreement is autoVerifyAgreement's synchronous core, shared with
// the explicit negotiationConsumerVerifyHandler (#83). Same shape as
// acceptOffer.
func verifyAgreement(n *Negotiation) (*Negotiation, error) {
	if err := sendAgreementVerification(n); err != nil {
		return n, err
	}

	verified := n.clone()
	if err := verified.transition(StateVerified, StateAgreed); err != nil {
		return n, err
	}
	if err := store.Save(verified); err != nil {
		return n, err
	}
	return verified, nil
}

package main

import (
	"encoding/json"
	"net/http"
)

// negotiationConsumerAcceptHandler implements
// POST /internal/v1/negotiations/consumer/{id}/accept - #83's explicit
// counterpart to #82's autoAcceptOffer, only meaningful when
// consumerAutoNegotiate is false (main.go's own doc comment): sends the
// outbound ACCEPTED event and transitions OFFERED -> ACCEPTED on demand,
// instead of automatically. Precondition (must be OFFERED) is checked here,
// before any outbound call, rather than inside acceptOffer - no point
// spending a real HTTP round trip to the Provider only to find out the
// local transition would have been rejected anyway.
func negotiationConsumerAcceptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}
	if n.State != StateOffered {
		writeInternalError(w, http.StatusConflict, "invalid-transition", "accept is only valid from OFFERED")
		return
	}

	accepted, err := acceptOffer(n)
	if err != nil {
		logger.Sugar().Errorw("explicit accept failed", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		writeInternalError(w, http.StatusBadGateway, "provider-request-failed", err.Error())
		return
	}

	writeNegotiation(w, http.StatusOK, accepted)
}

// negotiationConsumerVerifyHandler implements
// POST /internal/v1/negotiations/consumer/{id}/verify - #83's explicit
// counterpart to #82's autoVerifyAgreement. Same shape as
// negotiationConsumerAcceptHandler, AGREED -> VERIFIED.
func negotiationConsumerVerifyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}
	if n.State != StateAgreed {
		writeInternalError(w, http.StatusConflict, "invalid-transition", "verify is only valid from AGREED")
		return
	}

	verified, err := verifyAgreement(n)
	if err != nil {
		logger.Sugar().Errorw("explicit verify failed", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		writeInternalError(w, http.StatusBadGateway, "provider-request-failed", err.Error())
		return
	}

	writeNegotiation(w, http.StatusOK, verified)
}

// negotiationConsumerCounterBody carries the (possibly different) offer to
// counter-propose - opaque to negotiation-service, same as every other
// offer body in this package.
type negotiationConsumerCounterBody struct {
	Offer json.RawMessage `json:"offer"`
}

// negotiationConsumerCounterHandler implements
// POST /internal/v1/negotiations/consumer/{id}/counter - #83's new
// capability, never built before this: DYNAMOS-as-Consumer proposing
// different terms instead of accepting. Only reachable with
// consumerAutoNegotiate false - accept-all has no basis to ever counter.
// Valid only from OFFERED, same predecessor as accept - a counter-offer is
// the other real reaction to receiving an Offer. Transitions OFFERED ->
// REQUESTED, mirroring the Provider-role's own handling of an *inbound*
// counter-request (negotiationRequestHandler, T2.2) - sending one resets
// the negotiation to REQUESTED on the sender's side too, the DSP spec's own
// symmetric shape for a counter round.
func negotiationConsumerCounterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationConsumerCounterBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if len(body.Offer) == 0 {
		writeInternalError(w, http.StatusBadRequest, "missing-offer", "offer is required")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}
	if n.State != StateOffered {
		writeInternalError(w, http.StatusConflict, "invalid-transition", "counter is only valid from OFFERED")
		return
	}

	if err := sendCounterRequest(n, body.Offer); err != nil {
		logger.Sugar().Errorw("explicit counter failed", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		writeInternalError(w, http.StatusBadGateway, "provider-request-failed", err.Error())
		return
	}

	if !transitionOrError(w, n, StateRequested, StateOffered) {
		return
	}
	n.Offer = body.Offer
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationConsumerTerminateOutboundHandler implements
// POST /internal/v1/negotiations/consumer/{id}/terminate - #83's new
// capability: DYNAMOS-as-Consumer deciding to terminate, a capability that
// didn't exist anywhere before this (deliverToConsumer only ever pushes
// Provider-initiated messages the other way). Only reachable with
// consumerAutoNegotiate false. Valid from any non-terminal, non-FINALIZED
// state, same predecessor set as the inbound
// negotiationConsumerTerminationHandler uses.
func negotiationConsumerTerminateOutboundHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}

	if err := sendConsumerTermination(n); err != nil {
		logger.Sugar().Errorw("explicit outbound termination failed", "consumerPid", n.ConsumerPid, "providerPid", n.ProviderPid, "error", err)
		writeInternalError(w, http.StatusBadGateway, "provider-request-failed", err.Error())
		return
	}

	if !transitionOrError(w, n, StateTerminated,
		StateRequested, StateOffered, StateAccepted, StateAgreed, StateVerified) {
		return
	}
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

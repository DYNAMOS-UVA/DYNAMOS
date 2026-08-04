package main

import (
	"encoding/json"
	"net/http"
)

// negotiationConsumerRequestBody is create-as-consumer's request body -
// DYNAMOS itself initiating a Contract Request Message to an external
// Provider (#80). Unlike negotiationRequestBody (Provider-role, T2.2), every
// field here is required: there's no re-entrant/counter variant yet (that's
// #82's job, once real decision logic exists to drive one).
type negotiationConsumerRequestBody struct {
	// ProviderEndpoint is the remote dsp-connector's DSP base URL - the
	// outbound Contract Request Message goes to
	// ProviderEndpoint+"/negotiations/request".
	ProviderEndpoint string `json:"providerEndpoint"`
	// Participant is DYNAMOS's own identity, sent as the Authorization
	// header on the outbound message (see Negotiation.Participant's doc).
	Participant string `json:"participant"`
	// RemoteParticipant is the remote Provider's identity - see
	// Negotiation.RemoteParticipant. Required: the caller already has to
	// know who ProviderEndpoint belongs to in order to pick it.
	RemoteParticipant string          `json:"remoteParticipant"`
	CallbackAddress   string          `json:"callbackAddress"`
	Offer             json.RawMessage `json:"offer"`
}

// negotiationsConsumerCollectionHandler implements
// POST /internal/v1/negotiations/consumer - DYNAMOS-as-Consumer sends the
// initiating Contract Request Message to a remote Provider and, on success,
// persists the resulting negotiation in REQUESTED. Unlike every other
// handler in this package, this one makes an outbound DSP call itself
// (sendContractRequest) before it can even build a valid Negotiation - a
// Consumer-role negotiation doesn't exist locally until the remote Provider
// hands back a providerPid, so a failed outbound call must not persist
// anything.
func negotiationsConsumerCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationConsumerRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if body.ProviderEndpoint == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-provider-endpoint", "providerEndpoint is required")
		return
	}
	if body.Participant == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-participant", "participant is required")
		return
	}
	if body.RemoteParticipant == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-remote-participant", "remoteParticipant is required")
		return
	}
	if body.CallbackAddress == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-callback-address", "callbackAddress is required")
		return
	}
	if len(body.Offer) == 0 {
		writeInternalError(w, http.StatusBadRequest, "missing-offer", "offer is required")
		return
	}

	n := newConsumerNegotiation(party, body.Participant, body.RemoteParticipant, body.ProviderEndpoint, body.CallbackAddress, body.Offer)

	providerPid, err := sendContractRequest(body.ProviderEndpoint, body.Participant, n)
	if err != nil {
		logger.Sugar().Errorw("outbound Contract Request Message failed", "consumerPid", n.ConsumerPid, "providerEndpoint", body.ProviderEndpoint, "error", err)
		writeInternalError(w, http.StatusBadGateway, "provider-request-failed", err.Error())
		return
	}
	n.ProviderPid = providerPid

	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusCreated, n)
}

// negotiationConsumerHandler implements
// GET /internal/v1/negotiations/consumer/{id} - the Consumer-role
// counterpart to negotiationHandler; id is the ConsumerPid (see
// Negotiation.OwnPid).
func negotiationConsumerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationConsumerOfferBody carries an inbound Contract Offer Message's
// `offer`.
type negotiationConsumerOfferBody struct {
	Offer json.RawMessage `json:"offer"`
}

// negotiationConsumerOfferHandler implements
// POST /internal/v1/negotiations/consumer/{id}/offer - dsp-connector calls
// this once it receives an inbound Contract Offer Message on the Consumer
// callback endpoint (#81). -> OFFERED, valid from REQUESTED (first offer) or
// OFFERED (counter-offer), same predecessor set as the Provider-role
// negotiationOfferHandler.
//
// Once OFFERED is durably saved, #82's autoAcceptOffer fires in the
// background, trivially accepting every Offer and sending the outbound
// ACCEPTED event - async (see autoAcceptOffer's own doc on why: a
// synchronous outbound call back to the Provider that just sent this same
// Offer raced its own embedded server in a live TCK run). The response
// always reports OFFERED - the DSP HTTPS binding doesn't require this ack
// to carry the post-auto-accept state, a GET afterwards is how a caller
// observes whether it reached ACCEPTED.
func negotiationConsumerOfferHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationConsumerOfferBody
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

	if !transitionOrError(w, n, StateOffered, StateRequested, StateOffered) {
		return
	}
	n.Offer = body.Offer
	if !saveOrError(w, n) {
		return
	}

	autoAcceptOffer(n)

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationConsumerAgreementBody carries an inbound Contract Agreement
// Message's full `agreement` object.
type negotiationConsumerAgreementBody struct {
	Agreement json.RawMessage `json:"agreement"`
}

// negotiationConsumerAgreementHandler implements
// POST /internal/v1/negotiations/consumer/{id}/agreement -> AGREED. Valid
// from ACCEPTED (the usual Offer/Accept round) or REQUESTED directly, same
// predecessor set the Provider-role negotiationAgreementHandler uses (T2.5's
// DSP TCK finding applies symmetrically here: the spec lets a Provider agree
// outright, skipping the Offer/Accept exchange).
//
// Once AGREED is durably saved, #82's autoVerifyAgreement fires in the
// background, same async shape as negotiationConsumerOfferHandler's
// autoAcceptOffer call - the response always reports AGREED, a GET
// afterwards is how a caller observes whether it reached VERIFIED.
func negotiationConsumerAgreementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationConsumerAgreementBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if len(body.Agreement) == 0 {
		writeInternalError(w, http.StatusBadRequest, "missing-agreement", "agreement is required")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, n, StateAgreed, StateAccepted, StateRequested) {
		return
	}
	n.Agreement = body.Agreement
	if !saveOrError(w, n) {
		return
	}

	autoVerifyAgreement(n)

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationConsumerEventBody carries an inbound Contract Negotiation Event
// Message's `eventType` - only FINALIZED is ever received here: ACCEPTED is
// sent by DYNAMOS-as-Consumer (#82), never received, the same asymmetry
// dsp-negotiation-consumer-state-machine.md documents.
type negotiationConsumerEventBody struct {
	EventType string `json:"eventType"`
}

// negotiationConsumerEventsHandler implements
// POST /internal/v1/negotiations/consumer/{id}/events -> FINALIZED, valid
// from VERIFIED only. Any eventType other than FINALIZED is rejected -
// DYNAMOS-as-Consumer must never receive ACCEPTED, it is the one sending it.
func negotiationConsumerEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationConsumerEventBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if body.EventType != string(StateFinalized) {
		writeInternalError(w, http.StatusBadRequest, "invalid-event-type", "eventType must be FINALIZED")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, n, StateFinalized, StateVerified) {
		return
	}
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationConsumerTerminationHandler implements
// POST /internal/v1/negotiations/consumer/{id}/termination -> TERMINATED.
// Valid from any non-FINALIZED state, same reasoning as the Provider-role
// negotiationTerminationHandler (T2.5, DSP TCK CN:03-01): once FINALIZED,
// terminating no longer makes protocol sense.
func negotiationConsumerTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, KindConsumer, r.PathValue("id"))
	if !ok {
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

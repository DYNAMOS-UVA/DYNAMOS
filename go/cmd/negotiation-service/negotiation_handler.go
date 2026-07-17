package main

import (
	"encoding/json"
	"errors"
	"net/http"
)

// internalError mirrors catalog-service's own internal-API error shape - not
// a DSP ContractNegotiationError, since this contract is service-to-service
// only. dsp-connector (T2.3) maps these into the DSP shape on its side.
type internalError struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeInternalError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(internalError{Code: code, Error: msg})
}

func writeNegotiation(w http.ResponseWriter, status int, n *Negotiation) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(n)
}

// getNegotiationOrError fetches the negotiation and writes the right
// internal-API error on failure: not-found is a business 404, anything else
// (etcd I/O) is a 500 - callers only need to check ok.
func getNegotiationOrError(w http.ResponseWriter, id string) (*Negotiation, bool) {
	n, err := store.Get(id)
	if err == nil {
		return n, true
	}

	if errors.Is(err, ErrNegotiationNotFound) {
		writeInternalError(w, http.StatusNotFound, "negotiation-not-found", err.Error())
		return nil, false
	}

	logger.Sugar().Errorw("failed to fetch negotiation", "id", id, "error", err)
	writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to fetch negotiation data")
	return nil, false
}

// saveOrError persists n and writes a 500 internal-API error on failure.
func saveOrError(w http.ResponseWriter, n *Negotiation) bool {
	if err := store.Save(n); err != nil {
		logger.Sugar().Errorw("failed to save negotiation", "id", n.ProviderPid, "error", err)
		writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to save negotiation data")
		return false
	}
	return true
}

// transitionOrError runs n.transition and writes the right internal-API
// error (409) if the current state doesn't allow it.
func transitionOrError(w http.ResponseWriter, n *Negotiation, to State, from ...State) bool {
	if err := n.transition(to, from...); err != nil {
		writeInternalError(w, http.StatusConflict, "invalid-transition", err.Error())
		return false
	}
	return true
}

// negotiationRequestBody is the shared body shape for the two Contract
// Request Message endpoints (initiating and counter) - `offer` carries the
// (possibly countered) ODRL offer, opaque to negotiation-service. Participant
// is only meaningful (and required) on the initiating endpoint - a
// counter-request never changes who owns the negotiation, so it's ignored
// there.
type negotiationRequestBody struct {
	ConsumerPid string          `json:"consumerPid"`
	Participant string          `json:"participant"`
	Offer       json.RawMessage `json:"offer"`
}

// negotiationsCollectionHandler implements
// POST /internal/v1/negotiations (Contract Request Message, initiating,
// no providerPid yet) -> REQUESTED.
func negotiationsCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if body.ConsumerPid == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-consumer-pid", "consumerPid is required")
		return
	}
	if body.Participant == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-participant", "participant is required")
		return
	}
	if len(body.Offer) == 0 {
		writeInternalError(w, http.StatusBadRequest, "missing-offer", "offer is required")
		return
	}

	n := newNegotiation(party, body.ConsumerPid, body.Participant, body.Offer)
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusCreated, n)
}

// negotiationHandler implements GET /internal/v1/negotiations/{id} - the
// read-only counterpart to the DSP GET /negotiations/:providerPid endpoint.
func negotiationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationRequestHandler implements
// POST /internal/v1/negotiations/{id}/request (Contract Request Message,
// counter-request) -> REQUESTED, re-entrant. Only valid from OFFERED - a
// negotiation can loop OFFERED <-> REQUESTED before either side accepts.
func negotiationRequestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, n, StateRequested, StateOffered) {
		return
	}
	if len(body.Offer) > 0 {
		n.Offer = body.Offer
	}
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationOfferBody carries the Contract Offer Message's `offer` (with
// `target`, per the doc's message table).
type negotiationOfferBody struct {
	Offer json.RawMessage `json:"offer"`
}

// negotiationOfferHandler implements
// POST /internal/v1/negotiations/{id}/offer (Contract Offer Message)
// -> OFFERED. Valid from REQUESTED (first offer) or OFFERED (counter-offer).
func negotiationOfferHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationOfferBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if len(body.Offer) == 0 {
		writeInternalError(w, http.StatusBadRequest, "missing-offer", "offer is required")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
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

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationEventBody carries the Contract Negotiation Event Message's
// `eventType` - ACCEPTED (Consumer) or FINALIZED (Provider).
type negotiationEventBody struct {
	EventType string `json:"eventType"`
}

// negotiationEventsHandler implements
// POST /internal/v1/negotiations/{id}/events (Contract Negotiation Event
// Message) -> ACCEPTED (from OFFERED) or FINALIZED (from VERIFIED). Any
// other eventType is rejected - cross-sending (Consumer FINALIZED / Provider
// ACCEPTED) is a protocol error, not this service's call to make either way.
func negotiationEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationEventBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	switch body.EventType {
	case string(StateAccepted):
		if !transitionOrError(w, n, StateAccepted, StateOffered) {
			return
		}
	case string(StateFinalized):
		if !transitionOrError(w, n, StateFinalized, StateVerified) {
			return
		}
		// Write the negotiated agreement into policy-enforcer's own etcd key
		// before persisting FINALIZED (issue #47) - if this fails, the
		// negotiation must stay VERIFIED (n is never saved below) so the
		// caller can retry FINALIZED rather than believe access was granted
		// when it wasn't.
		if err := applyPolicyEnforcement(n); err != nil {
			logger.Sugar().Errorw("failed to apply policy enforcement for finalized negotiation", "id", n.ProviderPid, "party", n.Party, "error", err)
			writeInternalError(w, http.StatusInternalServerError, "policy-enforcement-failed", "failed to record the negotiated agreement for policy enforcement")
			return
		}
	default:
		writeInternalError(w, http.StatusBadRequest, "invalid-event-type", "eventType must be ACCEPTED or FINALIZED")
		return
	}

	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationAgreementBody carries the Contract Agreement Message's full
// `agreement` object (`@id`, `target`, `assigner`/`assignee`,
// `permission`/`prohibition`/`obligation`) - stored opaque, T2.4 is what
// interprets it into a policy-enforcer Relation.
type negotiationAgreementBody struct {
	Agreement json.RawMessage `json:"agreement"`
}

// negotiationAgreementHandler implements
// POST /internal/v1/negotiations/{id}/agreement (Contract Agreement Message)
// -> AGREED. Valid from ACCEPTED only.
func negotiationAgreementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body negotiationAgreementBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}
	if len(body.Agreement) == 0 {
		writeInternalError(w, http.StatusBadRequest, "missing-agreement", "agreement is required")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, n, StateAgreed, StateAccepted) {
		return
	}
	n.Agreement = body.Agreement
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationVerificationHandler implements
// POST /internal/v1/negotiations/{id}/agreement/verification (Contract
// Agreement Verification Message) -> VERIFIED. Valid from AGREED only.
func negotiationVerificationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, n, StateVerified, StateAgreed) {
		return
	}
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

// negotiationTerminationHandler implements
// POST /internal/v1/negotiations/{id}/termination (Contract Negotiation
// Termination Message) -> TERMINATED. Valid from any state including
// FINALIZED (per the spec, termination needs no explanation and is valid
// from any state) - only TERMINATED itself is a dead end, enforced by
// transition() regardless of the `from` list passed here.
func negotiationTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	n, ok := getNegotiationOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, n, StateTerminated,
		StateRequested, StateOffered, StateAccepted, StateAgreed, StateVerified, StateFinalized) {
		return
	}
	if !saveOrError(w, n) {
		return
	}

	writeNegotiation(w, http.StatusOK, n)
}

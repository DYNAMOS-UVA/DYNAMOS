package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// negotiationConsumerOfferMessage mirrors the DSP ContractOfferMessage shape
// received on the Consumer callback (contractRequestMessage's comment
// explains why @context/@type aren't decoded).
type negotiationConsumerOfferMessage struct {
	ProviderPid string          `json:"providerPid"`
	ConsumerPid string          `json:"consumerPid"`
	Offer       json.RawMessage `json:"offer"`
}

// negotiationConsumerAgreementMessage mirrors the DSP
// ContractAgreementMessage shape received on the Consumer callback.
type negotiationConsumerAgreementMessage struct {
	ProviderPid string          `json:"providerPid"`
	ConsumerPid string          `json:"consumerPid"`
	Agreement   json.RawMessage `json:"agreement"`
}

// negotiationConsumerEventMessage mirrors the DSP
// ContractNegotiationEventMessage shape received on the Consumer callback -
// only eventType FINALIZED is ever valid here, the Provider-sent
// counterpart to negotiationEventMessage's ACCEPTED (see
// docs/negotiation/dsp-negotiation-consumer-state-machine.md's
// provider/consumer endpoint asymmetry note).
type negotiationConsumerEventMessage struct {
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
	EventType   string `json:"eventType"`
}

// negotiationConsumerTerminationMessage mirrors the DSP
// ContractNegotiationTerminationMessage shape received on the Consumer
// callback - code/reason are accepted (so a well-formed message round-trips)
// but only logged, same as the Provider-role negotiationTerminationMessage.
type negotiationConsumerTerminationMessage struct {
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code,omitempty"`
	Reason      []interface{} `json:"reason,omitempty"`
}

// writeConsumerNegotiationAck mirrors writeNegotiationAck for a
// consumerNegotiationRecord - the two record types carry the same 3 fields
// this ack needs (providerPid/consumerPid/state), just fetched from
// negotiation-service's Consumer-role internal API instead of its
// Provider-role one.
func writeConsumerNegotiationAck(w http.ResponseWriter, status int, n *consumerNegotiationRecord) {
	writeNegotiationAck(w, status, &negotiationRecord{
		ProviderPid: n.ProviderPid,
		ConsumerPid: n.ConsumerPid,
		State:       n.State,
	})
}

// mapConsumerNegotiationServiceError is mapNegotiationServiceError's
// Consumer-role counterpart: same sentinel-to-status mapping
// (negotiation_client.go's ErrNegotiationNotFound/ErrNegotiationForbidden/
// ErrNegotiationInvalidTransition are shared by both record types, only the
// HTTP call that produced them differs), reused as-is.
func mapConsumerNegotiationServiceError(w http.ResponseWriter, providerPid, consumerPid string, err error) {
	mapNegotiationServiceError(w, providerPid, consumerPid, err)
}

// negotiationConsumerOffersHandler implements
// POST /:callback/negotiations/:consumerPid/offers (Contract Offer Message)
// per the DSP HTTPS binding's Consumer Path Bindings (#81). Records the
// inbound Offer via negotiation-service's Consumer-role internal API
// (negotiation_consumer_client.go, #80) - does not decide whether to accept
// it or send the outbound ACCEPTED event, that's #82's autonomous
// accept-all logic.
func negotiationConsumerOffersHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg negotiationConsumerOfferMessage
	if err := decodeConsumerCallbackBody(r, &msg); err != nil {
		writeNegotiationError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
		return
	}
	if len(msg.Offer) == 0 {
		writeNegotiationError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", "offer is required")
		return
	}

	existing, err := fetchConsumerNegotiation(consumerPid)
	if err != nil {
		mapConsumerNegotiationServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerNegotiationOwnership(existing, participant); err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeNegotiationError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this negotiation.")
		return
	}

	n, err := recordConsumerOffer(consumerPid, msg.Offer)
	if err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeConsumerNegotiationAck(w, http.StatusOK, n)
}

// negotiationConsumerAgreementHandler implements
// POST /:callback/negotiations/:consumerPid/agreement (Contract Agreement
// Message) per the Consumer Path Bindings.
func negotiationConsumerAgreementHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg negotiationConsumerAgreementMessage
	if err := decodeConsumerCallbackBody(r, &msg); err != nil {
		writeNegotiationError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
		return
	}
	if len(msg.Agreement) == 0 {
		writeNegotiationError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", "agreement is required")
		return
	}

	existing, err := fetchConsumerNegotiation(consumerPid)
	if err != nil {
		mapConsumerNegotiationServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerNegotiationOwnership(existing, participant); err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeNegotiationError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this negotiation.")
		return
	}

	n, err := recordConsumerAgreement(consumerPid, msg.Agreement)
	if err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeConsumerNegotiationAck(w, http.StatusOK, n)
}

// negotiationConsumerEventsHandler implements
// POST /:callback/negotiations/:consumerPid/events (Contract Negotiation
// Event Message) per the Consumer Path Bindings. Only eventType FINALIZED
// is valid here - ACCEPTED is Consumer-sent (DYNAMOS's own outbound call,
// #82), never received; a Provider sending ACCEPTED is a protocol
// violation, rejected as 400 per the spec's cross-sending rule (mirrors
// negotiationEventsHandler's own ACCEPTED-only check for the Provider
// side).
func negotiationConsumerEventsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg negotiationConsumerEventMessage
	if err := decodeConsumerCallbackBody(r, &msg); err != nil {
		writeNegotiationError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
		return
	}
	if msg.EventType != "FINALIZED" {
		writeNegotiationError(w, http.StatusBadRequest, msg.ProviderPid, consumerPid, "invalid-event-type", "Only eventType FINALIZED may be sent by a Provider to this endpoint.")
		return
	}

	existing, err := fetchConsumerNegotiation(consumerPid)
	if err != nil {
		mapConsumerNegotiationServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerNegotiationOwnership(existing, participant); err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeNegotiationError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this negotiation.")
		return
	}

	n, err := recordConsumerFinalized(consumerPid)
	if err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeConsumerNegotiationAck(w, http.StatusOK, n)
}

// negotiationConsumerTerminationHandler implements
// POST /:callback/negotiations/:consumerPid/termination (Contract
// Negotiation Termination Message) per the Consumer Path Bindings.
func negotiationConsumerTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg negotiationConsumerTerminationMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeNegotiationError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
			return
		}
	}
	if msg.Code != "" || len(msg.Reason) > 0 {
		logger.Sugar().Infow("Negotiation termination received", "consumerPid", consumerPid, "code", msg.Code, "reason", msg.Reason)
	}

	existing, err := fetchConsumerNegotiation(consumerPid)
	if err != nil {
		mapConsumerNegotiationServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerNegotiationOwnership(existing, participant); err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeNegotiationError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this negotiation.")
		return
	}

	n, err := recordConsumerTermination(consumerPid)
	if err != nil {
		mapConsumerNegotiationServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeConsumerNegotiationAck(w, http.StatusOK, n)
}

// decodeConsumerCallbackBody decodes r's body into v, mapping a missing body
// to the same "request body is required" message every handler in this file
// uses - shared so each handler doesn't repeat the nil-check/EOF-check pair.
func decodeConsumerCallbackBody(r *http.Request, v any) error {
	if r.Body == nil {
		return errMissingRequestBody
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errMissingRequestBody
		}
		return err
	}
	return nil
}

var errMissingRequestBody = errors.New("request body is required")

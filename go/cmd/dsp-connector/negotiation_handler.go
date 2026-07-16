package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
)

// ErrInvalidOffer / ErrOfferNotFound: sentinels for offer.@id validation
// against catalog-service (decision made in the Phase 2 drafting session:
// dsp-connector validates the offer using its existing catalog-service
// client - negotiation-service stays free of any catalog dependency).
var (
	ErrInvalidOffer  = errors.New("offer is malformed")
	ErrOfferNotFound = errors.New("offer.@id does not match any offer in the requester's catalog")
)

// contractRequestMessage mirrors the DSP ContractRequestMessage shape
// (docs/negotiation/spec-reference/negotiation/contract-request-message-schema.json).
// Used for both the initiating request (POST /negotiations/request, no
// providerPid) and the counter-request (POST /negotiations/:providerPid/request).
type contractRequestMessage struct {
	Context         interface{}     `json:"@context"`
	Type            string          `json:"@type"`
	ConsumerPid     string          `json:"consumerPid"`
	ProviderPid     string          `json:"providerPid,omitempty"`
	Offer           json.RawMessage `json:"offer"`
	CallbackAddress string          `json:"callbackAddress,omitempty"`
}

// negotiationEventMessage mirrors the DSP ContractNegotiationEventMessage
// shape. This endpoint only ever receives eventType ACCEPTED (Consumer-sent)
// - FINALIZED is Provider-sent, delivered to the Consumer's callback, never
// received here (see docs/negotiation/dsp-negotiation-state-machine.md's
// provider/consumer endpoint asymmetry note).
type negotiationEventMessage struct {
	Context     interface{} `json:"@context"`
	Type        string      `json:"@type"`
	ProviderPid string      `json:"providerPid"`
	ConsumerPid string      `json:"consumerPid"`
	EventType   string      `json:"eventType"`
}

// negotiationTerminationMessage mirrors the DSP
// ContractNegotiationTerminationMessage shape. Code/reason are accepted (so
// a well-formed message round-trips) but only logged - negotiation-service
// doesn't persist termination reasons, same as its own internal API.
type negotiationTerminationMessage struct {
	Context     interface{}   `json:"@context"`
	Type        string        `json:"@type"`
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code,omitempty"`
	Reason      []interface{} `json:"reason,omitempty"`
}

// offerRef is the minimal shape needed out of an ODRL Offer to validate it
// against the requester's real catalog - @id must match one of the Offers in
// their Catalog, target (if present) must match that Offer's Dataset.
type offerRef struct {
	ID     string `json:"@id"`
	Target string `json:"target"`
}

// negotiationAck mirrors the DSP ContractNegotiation shape
// (docs/negotiation/spec-reference/negotiation/contract-negotiation-schema.json) -
// the ack body returned by every provider endpoint.
type negotiationAck struct {
	Context     []interface{} `json:"@context"`
	Type        string        `json:"@type"`
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	State       string        `json:"state"`
}

// negotiationError mirrors the DSP ContractNegotiationError shape
// (docs/negotiation/spec-reference/negotiation/contract-negotiation-error-schema.json).
type negotiationError struct {
	Context     []interface{} `json:"@context"`
	Type        string        `json:"@type"`
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code"`
	Reason      []string      `json:"reason"`
}

func writeNegotiationError(w http.ResponseWriter, status int, providerPid, consumerPid, code, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(negotiationError{
		Context:     catalog.Context,
		Type:        "ContractNegotiationError",
		ProviderPid: providerPid,
		ConsumerPid: consumerPid,
		Code:        code,
		Reason:      []string{reason},
	})
}

func writeNegotiationAck(w http.ResponseWriter, status int, n *negotiationRecord) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(negotiationAck{
		Context:     catalog.Context,
		Type:        "ContractNegotiation",
		ProviderPid: n.ProviderPid,
		ConsumerPid: n.ConsumerPid,
		State:       n.State,
	})
}

// mapNegotiationServiceError writes the right DSP-level response for an
// error returned by negotiation_client.go's calls, per the HTTPS binding's
// error rules (contract.negotiation.binding.https.md): 404 if the
// negotiation doesn't exist, 400 + ContractNegotiationError for an invalid
// state transition, 502 for anything else (network/upstream failure -
// mirrors catalog_handler.go's own "upstream-error" convention).
func mapNegotiationServiceError(w http.ResponseWriter, providerPid, consumerPid string, err error) {
	if errors.Is(err, ErrNegotiationNotFound) {
		logger.Sugar().Infow("Negotiation not found", "providerPid", providerPid, "error", err)
		writeNegotiationError(w, http.StatusNotFound, providerPid, consumerPid, "not-found", "Contract negotiation not found.")
		return
	}
	if errors.Is(err, ErrNegotiationInvalidTransition) {
		logger.Sugar().Infow("Negotiation state transition rejected", "providerPid", providerPid, "error", err)
		writeNegotiationError(w, http.StatusBadRequest, providerPid, consumerPid, "invalid-transition", "This message is not valid for the negotiation's current state.")
		return
	}
	logger.Sugar().Errorw("negotiation-service request failed", "providerPid", providerPid, "error", err)
	writeNegotiationError(w, http.StatusBadGateway, providerPid, consumerPid, "upstream-error", "Failed to reach negotiation-service.")
}

// validateOffer checks offer against participant's real catalog (decision:
// dsp-connector validates offer.@id via its existing catalog-service client;
// negotiation-service never sees or interprets the catalog at all).
func validateOffer(participant string, offer json.RawMessage) error {
	var ref offerRef
	if err := json.Unmarshal(offer, &ref); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOffer, err)
	}
	if ref.ID == "" {
		return fmt.Errorf("%w: offer.@id is required", ErrInvalidOffer)
	}

	cat, err := fetchCatalog(participant)
	if err != nil {
		return err
	}

	for _, ds := range cat.Dataset {
		for _, o := range ds.HasPolicy {
			if o.ID != ref.ID {
				continue
			}
			if ref.Target != "" && ref.Target != ds.ID {
				return fmt.Errorf("%w: target %q does not match offer %q's dataset %q", ErrInvalidOffer, ref.Target, ref.ID, ds.ID)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrOfferNotFound, ref.ID)
}

// decodeContractRequest decodes body into a contractRequestMessage and
// checks the fields every Contract Request Message needs regardless of
// initiating-vs-counter.
func decodeContractRequest(r *http.Request) (*contractRequestMessage, error) {
	var msg contractRequestMessage
	if r.Body == nil {
		return nil, fmt.Errorf("%w: request body is required", ErrInvalidOffer)
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: request body is required", ErrInvalidOffer)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidOffer, err)
	}
	if msg.ConsumerPid == "" {
		return nil, fmt.Errorf("%w: consumerPid is required", ErrInvalidOffer)
	}
	if len(msg.Offer) == 0 {
		return nil, fmt.Errorf("%w: offer is required", ErrInvalidOffer)
	}
	return &msg, nil
}

// negotiationRequestInitHandler implements POST /negotiations/request per
// the DSP Contract Negotiation HTTPS Binding: starts a new negotiation in
// REQUESTED, validating the offer against the requester's real catalog
// before creating anything in negotiation-service.
func negotiationRequestInitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	participant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, "", "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	msg, err := decodeContractRequest(r)
	if err != nil {
		writeNegotiationError(w, http.StatusBadRequest, "", "", "invalid-request", err.Error())
		return
	}

	if err := validateOffer(participant, msg.Offer); err != nil {
		if errors.Is(err, ErrOfferNotFound) || errors.Is(err, ErrInvalidOffer) {
			logger.Sugar().Infow("Contract request denied: invalid offer", "participant", participant, "error", err)
			writeNegotiationError(w, http.StatusBadRequest, "", msg.ConsumerPid, "invalid-offer", err.Error())
			return
		}
		if errors.Is(err, ErrParticipantNotFound) {
			logger.Sugar().Infow("Contract request denied: unprovisioned participant", "participant", participant, "error", err)
			writeNegotiationError(w, http.StatusBadRequest, "", msg.ConsumerPid, "invalid-offer", "Catalog not provisioned for this requester.")
			return
		}
		logger.Sugar().Errorw("catalog-service request failed", "participant", participant, "error", err)
		writeNegotiationError(w, http.StatusBadGateway, "", msg.ConsumerPid, "upstream-error", "Failed to validate offer against catalog.")
		return
	}

	n, err := createNegotiation(msg.ConsumerPid, msg.Offer)
	if err != nil {
		mapNegotiationServiceError(w, "", msg.ConsumerPid, err)
		return
	}

	writeNegotiationAck(w, http.StatusCreated, n)
}

// negotiationGetHandler implements GET /negotiations/:providerPid.
func negotiationGetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	providerPid := r.PathValue("providerPid")
	n, err := fetchNegotiation(providerPid)
	if err != nil {
		mapNegotiationServiceError(w, providerPid, "", err)
		return
	}

	writeNegotiationAck(w, http.StatusOK, n)
}

// negotiationRequestHandler implements POST /negotiations/:providerPid/request
// (counter-request) - same offer validation as the initiating endpoint.
func negotiationRequestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	msg, err := decodeContractRequest(r)
	if err != nil {
		writeNegotiationError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
		return
	}

	if err := validateOffer(participant, msg.Offer); err != nil {
		if errors.Is(err, ErrOfferNotFound) || errors.Is(err, ErrInvalidOffer) || errors.Is(err, ErrParticipantNotFound) {
			logger.Sugar().Infow("Counter-request denied: invalid offer", "participant", participant, "error", err)
			writeNegotiationError(w, http.StatusBadRequest, providerPid, msg.ConsumerPid, "invalid-offer", err.Error())
			return
		}
		logger.Sugar().Errorw("catalog-service request failed", "participant", participant, "error", err)
		writeNegotiationError(w, http.StatusBadGateway, providerPid, msg.ConsumerPid, "upstream-error", "Failed to validate offer against catalog.")
		return
	}

	n, err := counterRequestNegotiation(providerPid, msg.Offer)
	if err != nil {
		mapNegotiationServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeNegotiationAck(w, http.StatusOK, n)
}

// negotiationEventsHandler implements POST /negotiations/:providerPid/events.
// Only eventType ACCEPTED is valid here - FINALIZED is Provider-sent
// (delivered to the Consumer's callback), never received on this endpoint;
// a Consumer sending FINALIZED is a protocol violation, rejected as 400 per
// the spec's cross-sending rule.
func negotiationEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	providerPid := r.PathValue("providerPid")

	var msg negotiationEventMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeNegotiationError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
			return
		}
	}

	if msg.EventType != "ACCEPTED" {
		writeNegotiationError(w, http.StatusBadRequest, providerPid, msg.ConsumerPid, "invalid-event-type", "Only eventType ACCEPTED may be sent by a Consumer to this endpoint.")
		return
	}

	n, err := acceptNegotiation(providerPid)
	if err != nil {
		mapNegotiationServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeNegotiationAck(w, http.StatusOK, n)
}

// negotiationVerificationHandler implements
// POST /negotiations/:providerPid/agreement/verification.
func negotiationVerificationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	providerPid := r.PathValue("providerPid")
	n, err := verifyNegotiationAgreement(providerPid)
	if err != nil {
		mapNegotiationServiceError(w, providerPid, "", err)
		return
	}

	writeNegotiationAck(w, http.StatusOK, n)
}

// negotiationTerminationHandler implements
// POST /negotiations/:providerPid/termination. code/reason are logged only.
func negotiationTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	providerPid := r.PathValue("providerPid")

	var msg negotiationTerminationMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeNegotiationError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
			return
		}
	}
	if msg.Code != "" || len(msg.Reason) > 0 {
		logger.Sugar().Infow("Negotiation termination requested", "providerPid", providerPid, "code", msg.Code, "reason", msg.Reason)
	}

	n, err := terminateNegotiation(providerPid)
	if err != nil {
		mapNegotiationServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeNegotiationAck(w, http.StatusOK, n)
}

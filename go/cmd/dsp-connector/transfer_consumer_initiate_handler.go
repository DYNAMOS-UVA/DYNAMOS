package main

import (
	"encoding/json"
	"errors"
	"net/http"
)

// transferConsumerInitiateBody is issue #93's own signal-to-initiate shape
// for transfers - not a DSP protocol message, DYNAMOS's own convention for
// "start a transfer now", the transfer-side counterpart to
// negotiationConsumerInitiateBody. NegotiationId names the FINALIZED
// Consumer-role negotiation this transfer runs under (its own ConsumerPid,
// as returned by the #80-#87 negotiation flow) - the remote Provider's
// identity, DSP base URL, and the Agreement to transfer under all come
// from that stored negotiation, not from this body. Format has no local
// source to derive it from (the Consumer side never fetches the remote
// Provider's catalog), so the caller supplies it directly - same
// caller-supplies-what-it-already-knows shape negotiationConsumerInitiateBody
// uses for datasetId/offerId.
type transferConsumerInitiateBody struct {
	NegotiationId string          `json:"negotiationId"`
	Format        string          `json:"format"`
	DataAddress   json.RawMessage `json:"dataAddress,omitempty"`
}

// transferConsumerInitiateHandler implements POST /transfers/initiate
// (issue #93): the only entry point in dsp-connector where DYNAMOS itself
// starts a Consumer-role transfer, sending the initiating Transfer Request
// Message to the FINALIZED negotiation's own Provider.
func transferConsumerInitiateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	// The remote Provider's identity and DSP base URL are already known:
	// captured on the FINALIZED negotiation itself when the #80-#87 flow
	// created it. The caller must be the identity that started that
	// negotiation: negotiationConsumerInitiateHandler stores its verified
	// caller as RemoteParticipant (the single-shared-Authorization
	// convention). Any other participant must not be able to start a
	// transfer under it.
	caller, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}
	if !mayInitiate(caller) {
		logger.Sugar().Infow("Transfer initiate denied: participant is not in CONSUMER_INITIATORS", "participant", caller)
		writeTransferError(w, http.StatusForbidden, "", "", "forbidden", "This participant may not start transfers at this connector.")
		return
	}

	var body transferConsumerInitiateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-request", "request body must be valid JSON")
		return
	}
	if body.NegotiationId == "" {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-request", "negotiationId is required")
		return
	}
	if body.Format == "" {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-request", "format is required")
		return
	}

	n, err := fetchConsumerNegotiation(body.NegotiationId)
	if err != nil {
		if errors.Is(err, ErrNegotiationNotFound) {
			writeTransferError(w, http.StatusBadRequest, "", "", "invalid-negotiation", "negotiationId does not name a known Consumer-role negotiation")
			return
		}
		logger.Sugar().Errorw("negotiation-service request failed", "negotiationId", body.NegotiationId, "error", err)
		writeTransferError(w, http.StatusBadGateway, "", "", "upstream-error", "Failed to look up negotiation.")
		return
	}
	if err := checkConsumerNegotiationOwnership(n, caller); err != nil {
		// Same answer as an unknown negotiation, so a caller cannot find
		// out which negotiationIds exist.
		logger.Sugar().Infow("Transfer initiate denied: caller does not own the negotiation", "negotiationId", body.NegotiationId, "participant", caller)
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-negotiation", "negotiationId does not name a known Consumer-role negotiation")
		return
	}
	if n.State != "FINALIZED" {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-negotiation", "negotiation "+body.NegotiationId+" is "+n.State+", not FINALIZED")
		return
	}
	if n.ProviderEndpoint == "" {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-negotiation", "negotiation has no providerEndpoint")
		return
	}

	// agreementId is the DSP TransferRequestMessage's own required field
	// (transfer-request-message-schema.json) - dsp-connector's Provider-role
	// validateAgreementId already established the convention that this is
	// the owning negotiation's own providerPid, not the Agreement object's
	// own @id (see transfer_handler.go's own doc comment on
	// validateAgreementId). n.ProviderPid is exactly that: the remote
	// Provider's own identifier for the negotiation this transfer runs
	// under, which its negotiation-service will look up when the remote
	// dsp-connector validates this transfer's agreementId.
	if n.ProviderPid == "" {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-negotiation", "negotiation has no providerPid")
		return
	}

	// ownParticipant is DYNAMOS's own identity on this transfer: the
	// verified DID of the caller (one of CONSUMER_INITIATORS, and the owner
	// of the negotiation). It is an identity, never the caller's token.
	// transfer-process-service sends its own credential (PARTY_DAT or STS)
	// on the outbound Transfer Request Message, so the remote Provider sees
	// the same identity that negotiated the agreement.
	ownParticipant := caller
	callbackAddress := connectorBaseURL + apiVersion + "/callback"

	t, err := createConsumerTransfer(body.NegotiationId, ownParticipant, body.Format, callbackAddress, body.DataAddress)
	if err != nil {
		mapTransferServiceError(w, "", "", err)
		return
	}

	writeTransferAck(w, http.StatusCreated, t)
}

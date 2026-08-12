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

	// Unlike negotiationConsumerInitiateHandler, this caller's identity is
	// never reused as the remote Provider's identity - there is no
	// TCK-style single-shared-Authorization convention for a real
	// two-DYNAMOS-instances transfer. This check only gates who may
	// trigger the call locally. The remote Provider's identity and DSP
	// base URL are already known: captured on the FINALIZED negotiation
	// itself when the #80-#87 flow created it.
	if _, ok := participantFromRequest(r); !ok {
		writeTransferError(w, http.StatusUnauthorized, "", "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
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

	// ownParticipant reuses the raw Authorization header this request
	// itself carried, same as negotiationConsumerInitiateHandler's own
	// fix (issue #93 live demo finding) - a real remote Provider
	// DAT-verifies its inbound Authorization header, so a synthesized
	// "urn:dynamos:party:X" label can never pass that check. n.ProviderPid
	// (the agreementId below) was validated on the remote Provider's
	// negotiation-service against n.Participant, which is this same real
	// DID - the outbound transfer request has to assert the identical
	// identity or validateAgreementId's ownership check fails there too.
	ownParticipant := r.Header.Get("Authorization")
	callbackAddress := connectorBaseURL + apiVersion + "/callback"

	t, err := createConsumerTransfer(n.ProviderEndpoint, ownParticipant, n.RemoteParticipant, n.ProviderPid, body.Format, callbackAddress, body.DataAddress)
	if err != nil {
		mapTransferServiceError(w, "", "", err)
		return
	}

	writeTransferAck(w, http.StatusCreated, t)
}

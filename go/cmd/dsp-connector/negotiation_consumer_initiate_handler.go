package main

import (
	"encoding/json"
	"net/http"
)

// negotiationConsumerInitiateBody mirrors the DSP TCK's own signal-to-
// initiate shape (HttpConsumerNegotiationClientImpl.initiateRequest in the
// TCK's own source, dsp-system/client/cn/http) - not a DSP protocol message,
// just this harness's convention for telling an external CUT "start a
// negotiation now" (#83). A real dataspace deployment would replace this
// with whatever actually decides DYNAMOS should negotiate for a given
// dataset - out of scope here, this endpoint exists purely so the DSP TCK's
// CN_C group has something real to call.
type negotiationConsumerInitiateBody struct {
	ProviderID       string `json:"providerId"`
	OfferID          string `json:"offerId"`
	DatasetID        string `json:"datasetId"`
	ConnectorAddress string `json:"connectorAddress"`
}

// negotiationConsumerInitiateHandler implements POST /negotiations/initiate
// (#83): the only entry point in dsp-connector where DYNAMOS itself starts a
// Consumer-role negotiation, sending the initiating Contract Request Message
// to connectorAddress.
//
// remoteParticipant is the DAT-verified caller of THIS request, not
// body.ProviderID - the TCK's own providerId field is just an internal
// label (DspConstants.TCK_PARTICIPANT_ID, "TCK_PARTICIPANT" in the TCK's own
// source) that ProviderNegotiationManagerImpl.handleContractRequest never
// checks against anything. Every later inbound Consumer callback (#81)
// authenticates with the same DAT the TCK sends here - a single shared
// Authorization header for the whole harness run
// (dataspacetck.dsp.connector.http.headers.authorization) - so storing that
// DAT identity now, not the body field, is what makes
// checkConsumerNegotiationOwnership later succeed instead of wrongly
// rejecting the real counterparty as a non-owner.
func negotiationConsumerInitiateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	remoteParticipant, ok := participantFromRequest(r)
	if !ok {
		writeNegotiationError(w, http.StatusUnauthorized, "", "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var body negotiationConsumerInitiateBody
	if err := decodeConsumerCallbackBody(r, &body); err != nil {
		writeNegotiationError(w, http.StatusBadRequest, "", "", "invalid-request", err.Error())
		return
	}
	if body.OfferID == "" {
		writeNegotiationError(w, http.StatusBadRequest, "", "", "invalid-request", "offerId is required")
		return
	}
	if body.ConnectorAddress == "" {
		writeNegotiationError(w, http.StatusBadRequest, "", "", "invalid-request", "connectorAddress is required")
		return
	}

	// offer.@id must equal offerId: the remote Provider derives its own
	// datasetId back out of this id (IdGenerator.offerIdFromDatasetId's
	// inverse, on the TCK's side) - datasetId is carried along too so the
	// offer at least resembles a real ODRL Offer referencing its Dataset.
	// @type/permission are required by the DSP TCK's own ContractRequestMessage
	// schema validation - confirmed live (#83), in two stages: a bare
	// {"@id","target"} offer failed with "required property 'permission'
	// not found, ...'prohibition'..., ...'@type'..."; adding @type plus
	// empty permission/prohibition arrays changed that to "must have at
	// least 1 items but found 0" on both - an Offer needs a real,
	// non-empty permission entry, not just the field present. One trivial
	// "use" permission with no constraints, no prohibition field at all,
	// mirrors catalog.Offer's own real shape (pkg/catalog/types.go) - the
	// same shape DYNAMOS's Provider-role endpoints already send, already
	// confirmed passing the CN group's own TCK validation (T2.5).
	offer, err := json.Marshal(map[string]any{
		"@id":    body.OfferID,
		"@type":  "Offer",
		"target": body.DatasetID,
		"permission": []map[string]any{
			{"action": "use"},
		},
	})
	if err != nil {
		writeNegotiationError(w, http.StatusInternalServerError, "", "", "internal-error", err.Error())
		return
	}

	ownParticipant := "urn:dynamos:party:" + party
	callbackAddress := connectorBaseURL + apiVersion + "/callback"

	n, err := createConsumerNegotiation(body.ConnectorAddress, ownParticipant, remoteParticipant, callbackAddress, offer)
	if err != nil {
		mapConsumerNegotiationServiceError(w, "", "", err)
		return
	}

	writeConsumerNegotiationAck(w, http.StatusCreated, n)
}

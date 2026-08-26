package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// transferConsumerRequestBody is create-as-consumer's request body -
// DYNAMOS itself initiating a Transfer Request Message to a remote
// Provider. Mirrors negotiation-service's negotiationConsumerRequestBody.
type transferConsumerRequestBody struct {
	// ProviderEndpoint is the remote dsp-connector's DSP base URL - the
	// outbound Transfer Request Message goes to
	// ProviderEndpoint+"/transfers/request".
	ProviderEndpoint string `json:"providerEndpoint"`
	// Participant is DYNAMOS's own identity, sent as the Authorization
	// header on the outbound message (see TransferProcess.Participant's
	// doc).
	Participant string `json:"participant"`
	// RemoteParticipant is the remote Provider's identity - see
	// TransferProcess.RemoteParticipant.
	RemoteParticipant string `json:"remoteParticipant"`
	// AgreementId names the FINALIZED negotiation this transfer runs
	// under. Per dsp-connector's own validateAgreementId convention,
	// this is that negotiation's own providerPid, as the remote Provider
	// knows it.
	AgreementId     string          `json:"agreementId"`
	Format          string          `json:"format"`
	CallbackAddress string          `json:"callbackAddress"`
	DataAddress     json.RawMessage `json:"dataAddress,omitempty"`
}

// transfersConsumerCollectionHandler implements
// POST /internal/v1/transfers/consumer - DYNAMOS-as-Consumer sends the
// initiating Transfer Request Message to a remote Provider and, on
// success, persists the resulting transfer in REQUESTED. Like
// negotiation-service's negotiationsConsumerCollectionHandler, this
// handler makes an outbound DSP call itself (sendTransferRequest) before
// it can build a valid TransferProcess - a Consumer-role transfer does
// not exist locally until the remote Provider hands back a providerPid,
// so a failed outbound call must not persist anything.
func transfersConsumerCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body transferConsumerRequestBody
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
	if body.AgreementId == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-agreement-id", "agreementId is required")
		return
	}
	if body.Format == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-format", "format is required")
		return
	}
	if body.CallbackAddress == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-callback-address", "callbackAddress is required")
		return
	}

	t := newConsumerTransferProcess(party, body.Participant, body.RemoteParticipant, body.ProviderEndpoint, body.AgreementId, body.Format, body.CallbackAddress, body.DataAddress)

	providerPid, err := sendTransferRequest(body.ProviderEndpoint, body.Participant, t)
	if err != nil {
		logger.Sugar().Errorw("outbound Transfer Request Message failed", "consumerPid", t.ConsumerPid, "providerEndpoint", body.ProviderEndpoint, "error", err)
		writeInternalError(w, http.StatusBadGateway, "provider-request-failed", err.Error())
		return
	}
	t.ProviderPid = providerPid

	if !saveConsumerOrError(w, t) {
		return
	}

	writeTransfer(w, http.StatusCreated, t)
}

// getConsumerTransferOrError fetches a Consumer-role transfer. Mirrors
// getTransferOrError, reading from the Consumer-role store namespace
// (store.GetConsumer).
func getConsumerTransferOrError(w http.ResponseWriter, id string) (*TransferProcess, bool) {
	t, err := store.GetConsumer(id)
	if err == nil {
		return t, true
	}

	if errors.Is(err, ErrTransferNotFound) {
		writeInternalError(w, http.StatusNotFound, "transfer-not-found", err.Error())
		return nil, false
	}

	logger.Sugar().Errorw("failed to fetch consumer transfer", "id", id, "error", err)
	writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to fetch transfer data")
	return nil, false
}

// saveConsumerOrError saves t to the Consumer-role store namespace
// (store.SaveConsumer). On failure, it writes a 500 internal-API error.
func saveConsumerOrError(w http.ResponseWriter, t *TransferProcess) bool {
	if err := store.SaveConsumer(t); err != nil {
		logger.Sugar().Errorw("failed to save consumer transfer", "id", t.ConsumerPid, "error", err)
		writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to save transfer data")
		return false
	}
	return true
}

// transferConsumerHandler implements
// GET /internal/v1/transfers/consumer/{id} - the Consumer-role
// counterpart to transferHandler; id is the ConsumerPid.
func transferConsumerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getConsumerTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	writeTransfer(w, http.StatusOK, t)
}

// transferConsumerStartBody carries an inbound Transfer Start Message's
// optional dataAddress - the actual pushed data.
type transferConsumerStartBody struct {
	DataAddress json.RawMessage `json:"dataAddress,omitempty"`
}

// transferConsumerStartHandler implements
// POST /internal/v1/transfers/consumer/{id}/start - dsp-connector calls
// this once it receives an inbound Transfer Start Message on the Consumer
// callback endpoint. -> STARTED, valid from REQUESTED (the normal
// Provider-driven initial start) or SUSPENDED (resume).
//
// Unlike negotiation's inbound offer/agreement handlers, no auto-decision
// policy runs here: transfer has no accept/reject/counter step, so an
// inbound Start is just recorded, never decided on. This is the real
// simplification transfer's Consumer-role has over negotiation's.
func transferConsumerStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body transferConsumerStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}

	t, ok := getConsumerTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateStarted, StateRequested, StateSuspended) {
		return
	}
	if len(body.DataAddress) > 0 {
		t.DataAddress = body.DataAddress
	}
	if !saveConsumerOrError(w, t) {
		return
	}

	writeTransfer(w, http.StatusOK, t)
}

// transferConsumerCompletionHandler implements
// POST /internal/v1/transfers/consumer/{id}/completion -> COMPLETED,
// valid from STARTED only.
func transferConsumerCompletionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getConsumerTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateCompleted, StateStarted) {
		return
	}
	if !saveConsumerOrError(w, t) {
		return
	}

	writeTransfer(w, http.StatusOK, t)
}

// transferConsumerSuspensionHandler implements
// POST /internal/v1/transfers/consumer/{id}/suspension -> SUSPENDED,
// valid from STARTED only.
func transferConsumerSuspensionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getConsumerTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateSuspended, StateStarted) {
		return
	}
	if !saveConsumerOrError(w, t) {
		return
	}

	writeTransfer(w, http.StatusOK, t)
}

// transferConsumerTerminationHandler implements
// POST /internal/v1/transfers/consumer/{id}/termination -> TERMINATED,
// valid from any non-terminal state.
func transferConsumerTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getConsumerTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateTerminated, StateRequested, StateStarted, StateSuspended) {
		return
	}
	if !saveConsumerOrError(w, t) {
		return
	}

	writeTransfer(w, http.StatusOK, t)
}

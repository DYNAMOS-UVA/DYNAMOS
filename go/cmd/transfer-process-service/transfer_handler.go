package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// internalError copies negotiation-service's own internal-API error
// shape. It is not a DSP TransferError: this contract runs
// service-to-service only. dsp-connector (T3.1.4) will map these into
// the DSP shape on its side.
type internalError struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeInternalError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(internalError{Code: code, Error: msg})
}

func writeTransfer(w http.ResponseWriter, status int, t *TransferProcess) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(t)
}

// getTransferOrError fetches the transfer. On failure, it writes the
// matching internal-API error. Not-found becomes a 404. Any other error,
// such as an etcd I/O failure, becomes a 500. Callers only need to check
// ok.
func getTransferOrError(w http.ResponseWriter, id string) (*TransferProcess, bool) {
	t, err := store.Get(id)
	if err == nil {
		return t, true
	}

	if errors.Is(err, ErrTransferNotFound) {
		writeInternalError(w, http.StatusNotFound, "transfer-not-found", err.Error())
		return nil, false
	}

	logger.Sugar().Errorw("failed to fetch transfer", "id", id, "error", err)
	writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to fetch transfer data")
	return nil, false
}

// saveOrError saves t. On failure, it writes a 500 internal-API error.
func saveOrError(w http.ResponseWriter, t *TransferProcess) bool {
	if err := store.Save(t); err != nil {
		logger.Sugar().Errorw("failed to save transfer", "id", t.ProviderPid, "error", err)
		writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to save transfer data")
		return false
	}
	return true
}

// transitionOrError calls t.transition. If the current state does not
// allow the move, it writes a 409 internal-API error.
func transitionOrError(w http.ResponseWriter, t *TransferProcess, to State, from ...State) bool {
	if err := t.transition(to, from...); err != nil {
		writeInternalError(w, http.StatusConflict, "invalid-transition", err.Error())
		return false
	}
	return true
}

// transferRequestBody is the request body for the Transfer Request
// Message endpoint. AgreementId, Format, and CallbackAddress match the
// DSP message's own required properties (see
// transfer-request-message-schema.json). Participant is a DYNAMOS-only
// addition. This follows the same convention as negotiation-service's
// negotiationRequestBody.
type transferRequestBody struct {
	ConsumerPid     string          `json:"consumerPid"`
	Participant     string          `json:"participant"`
	AgreementId     string          `json:"agreementId"`
	Format          string          `json:"format"`
	CallbackAddress string          `json:"callbackAddress"`
	DataAddress     json.RawMessage `json:"dataAddress,omitempty"`
}

// transfersCollectionHandler implements POST /internal/v1/transfers
// (Transfer Request Message) -> REQUESTED.
func transfersCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body transferRequestBody
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

	t := newTransferProcess(party, body.ConsumerPid, body.Participant, body.AgreementId, body.Format, body.CallbackAddress, body.DataAddress)
	if !saveOrError(w, t) {
		return
	}

	// T3.2: trigger the DYNAMOS job pipeline in the background. The
	// caller (dsp-connector) gets its 201 response right away. Job
	// completion arrives later, as its own outbound Start/Completion or
	// Termination delivery. See job_execution.go.
	go triggerJobAndDeliver(t.ProviderPid)

	writeTransfer(w, http.StatusCreated, t)
}

// transferHandler implements GET /internal/v1/transfers/{id}. It is the
// read-only counterpart to the DSP GET /transfers/:providerPid endpoint.
func transferHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	writeTransfer(w, http.StatusOK, t)
}

// transferStartBody carries the Transfer Start Message's optional
// dataAddress field. The prose spec requires dataAddress only for a pull
// transfer.
type transferStartBody struct {
	DataAddress json.RawMessage `json:"dataAddress,omitempty"`
}

// transferStartHandler implements POST /internal/v1/transfers/{id}/start
// (Transfer Start Message). It moves the transfer to STARTED.
//
// This move is valid from two different source states, sent by two
// different parties. This is the Provider-initiated-Start vs
// Consumer-resume-Start asymmetry (see
// docs/transfer/dsp-transfer-state-machine.md):
//   - REQUESTED -> STARTED: the initial start. The Provider sends this.
//     It is the normal happy path. dsp-connector's inbound provider-path
//     /start endpoint never triggers this case: no inbound route exists
//     for it.
//   - SUSPENDED -> STARTED: resume after suspend. The Consumer sends
//     this. dsp-connector's inbound /transfers/:providerPid/start
//     endpoint (T3.1.4) only ever forwards this case.
//
// Outbound delivery always fires, no matter which source state
// triggered it. This matches negotiation-service's termination handler.
// Delivery is harmless when the Consumer already knows, for example on
// resume, since they sent it. Delivery is required when the Consumer
// does not know, for example on the initial Provider-driven start.
func transferStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	var body transferStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeInternalError(w, http.StatusBadRequest, "invalid-body", "request body must be valid JSON")
		return
	}

	t, ok := getTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateStarted, StateRequested, StateSuspended) {
		return
	}
	if len(body.DataAddress) > 0 {
		t.DataAddress = body.DataAddress
	}
	if !saveOrError(w, t) {
		return
	}

	deliverToConsumer(t, "start", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferStartMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
		"dataAddress": t.DataAddress,
	})

	writeTransfer(w, http.StatusOK, t)
}

// transferCompletionHandler implements
// POST /internal/v1/transfers/{id}/completion (Transfer Completion
// Message). It moves the transfer to COMPLETED. This move is valid from
// STARTED only. The state diagram has one edge into COMPLETED: STARTED
// -> COMPLETED. SUSPENDED and TERMINATED both also reach back from
// STARTED, but COMPLETED does not.
func transferCompletionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateCompleted, StateStarted) {
		return
	}
	if !saveOrError(w, t) {
		return
	}

	deliverToConsumer(t, "completion", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferCompletionMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
	})

	writeTransfer(w, http.StatusOK, t)
}

// transferSuspensionHandler implements
// POST /internal/v1/transfers/{id}/suspension (Transfer Suspension
// Message). It moves the transfer to SUSPENDED. This move is valid from
// STARTED only, per the state machine doc.
func transferSuspensionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateSuspended, StateStarted) {
		return
	}
	if !saveOrError(w, t) {
		return
	}

	deliverToConsumer(t, "suspension", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferSuspensionMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
	})

	writeTransfer(w, http.StatusOK, t)
}

// transferTerminationHandler implements
// POST /internal/v1/transfers/{id}/termination (Transfer Termination
// Message). It moves the transfer to TERMINATED. This move is valid from
// any non-terminal state.
//
// COMPLETED is excluded. negotiation-service applies the same rule to
// FINALIZED: once a transfer has completed, terminating it no longer
// makes protocol sense. TERMINATED is the other dead end. transition()
// enforces that on its own, regardless of the `from` list passed here.
//
// This one endpoint serves both directions. dsp-connector forwards a
// Consumer-initiated termination here. transfer-process-service's own
// future callers, for example T3.2's job-failure path, will also call
// this endpoint to send a Provider-initiated termination. This handler
// always attempts delivery. Delivery is harmless for the
// Consumer-initiated case, since the Consumer already knows. Delivery
// is required for the Provider-initiated case. negotiation-service's own
// termination handler uses the same reasoning.
func transferTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInternalError(w, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed")
		return
	}

	t, ok := getTransferOrError(w, r.PathValue("id"))
	if !ok {
		return
	}

	if !transitionOrError(w, t, StateTerminated, StateRequested, StateStarted, StateSuspended) {
		return
	}
	if !saveOrError(w, t) {
		return
	}

	deliverToConsumer(t, "termination", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferTerminationMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
	})

	writeTransfer(w, http.StatusOK, t)
}

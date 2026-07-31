package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
)

// ErrInvalidAgreement and ErrAgreementNotFound are sentinels for agreementId
// validation against negotiation-service. Decision made here, in T3.1.4:
// dsp-connector validates agreementId using its existing negotiation-service
// client, the same shape as its existing offer.@id check against
// catalog-service (validateOffer, in negotiation_handler.go).
// transfer-process-service stays free of any negotiation-service dependency
// - see docs/transfer/dsp-transfer-state-machine.md's "Still open" section.
var (
	ErrInvalidAgreement  = errors.New("agreement is malformed")
	ErrAgreementNotFound = errors.New("agreementId does not match any FINALIZED negotiation owned by the requester")
)

// transferRequestMessage mirrors the DSP TransferRequestMessage shape
// (docs/transfer/spec-reference/transfer/transfer-request-message-schema.json).
// @context and @type still round-trip in the real DSP message but are not
// decoded here - this handler never re-emits them.
type transferRequestMessage struct {
	ConsumerPid     string          `json:"consumerPid"`
	AgreementId     string          `json:"agreementId"`
	Format          string          `json:"format"`
	DataAddress     json.RawMessage `json:"dataAddress,omitempty"`
	CallbackAddress string          `json:"callbackAddress"`
}

// transferStartMessage mirrors the DSP TransferStartMessage shape - only
// the fields this handler acts on. dsp-connector's inbound
// /transfers/:providerPid/start endpoint only ever receives the Consumer
// resume-after-suspend case (see transferStartHandler's own doc comment),
// so providerPid is not decoded: the path value already drives every
// operation here.
type transferStartMessage struct {
	ConsumerPid string          `json:"consumerPid"`
	DataAddress json.RawMessage `json:"dataAddress,omitempty"`
}

// transferCompletionMessage mirrors the DSP TransferCompletionMessage
// shape - just the identifiers, per the schema.
type transferCompletionMessage struct {
	ConsumerPid string `json:"consumerPid"`
}

// transferSuspensionMessage mirrors the DSP TransferSuspensionMessage
// shape. Code and reason are accepted, so a well-formed message round-
// trips. This handler only logs them. transfer-process-service does not
// store suspension reasons either - its own internal API works the same
// way.
type transferSuspensionMessage struct {
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code,omitempty"`
	Reason      []interface{} `json:"reason,omitempty"`
}

// transferTerminationMessage mirrors the DSP TransferTerminationMessage
// shape. Code and reason are logged only, same as transferSuspensionMessage.
type transferTerminationMessage struct {
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code,omitempty"`
	Reason      []interface{} `json:"reason,omitempty"`
}

// transferAck mirrors the DSP TransferProcess shape
// (docs/transfer/spec-reference/transfer/transfer-process-schema.json) -
// the ack body every provider endpoint returns.
type transferAck struct {
	Context     []interface{} `json:"@context"`
	Type        string        `json:"@type"`
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	State       string        `json:"state"`
}

// transferError mirrors the DSP TransferError shape
// (docs/transfer/spec-reference/transfer/transfer-error-schema.json).
type transferError struct {
	Context     []interface{} `json:"@context"`
	Type        string        `json:"@type"`
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code"`
	Reason      []string      `json:"reason"`
}

func writeTransferError(w http.ResponseWriter, status int, providerPid, consumerPid, code, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(transferError{
		Context:     catalog.Context,
		Type:        "TransferError",
		ProviderPid: providerPid,
		ConsumerPid: consumerPid,
		Code:        code,
		Reason:      []string{reason},
	})
}

func writeTransferAck(w http.ResponseWriter, status int, t *transferRecord) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(transferAck{
		Context:     catalog.Context,
		Type:        "TransferProcess",
		ProviderPid: t.ProviderPid,
		ConsumerPid: t.ConsumerPid,
		State:       t.State,
	})
}

// mapTransferServiceError writes the right DSP-level response for an error
// returned by transfer_client.go's calls. The rules come from the HTTPS
// binding's own error rules (transfer.process.binding.https.md):
//   - the transfer does not exist, or is not owned by the requester: 404
//   - the state transition is invalid: 400, with a TransferError
//   - anything else: 502
//
// This mirrors mapNegotiationServiceError's own rules.
func mapTransferServiceError(w http.ResponseWriter, providerPid, consumerPid string, err error) {
	if errors.Is(err, ErrTransferNotFound) || errors.Is(err, ErrTransferForbidden) {
		// A non-owner gets the same response as a truly unknown providerPid.
		// A distinct status or code here would let a caller tell "not yours"
		// apart from "does not exist", which would leak which providerPids
		// are real.
		logger.Sugar().Infow("Transfer not found or not owned by requester", "providerPid", providerPid, "error", err)
		writeTransferError(w, http.StatusNotFound, providerPid, consumerPid, "not-found", "Transfer process not found.")
		return
	}
	if errors.Is(err, ErrTransferInvalidTransition) {
		logger.Sugar().Infow("Transfer state transition rejected", "providerPid", providerPid, "error", err)
		writeTransferError(w, http.StatusBadRequest, providerPid, consumerPid, "invalid-transition", "This message is not valid for the transfer's current state.")
		return
	}
	logger.Sugar().Errorw("transfer-process-service request failed", "providerPid", providerPid, "error", err)
	writeTransferError(w, http.StatusBadGateway, providerPid, consumerPid, "upstream-error", "Failed to reach transfer-process-service.")
}

// validateAgreementId checks that agreementId names a FINALIZED negotiation
// owned by participant, using dsp-connector's existing negotiation-service
// client.
//
// This function sets a convention that
// docs/transfer/dsp-transfer-state-machine.md left open: DYNAMOS treats
// agreementId as the owning negotiation's own providerPid.
// negotiation-service stores the Agreement body as an opaque blob. It does
// not index that body by the Agreement's own @id. No other lookup path
// exists today. Revisit this if T3.4's real TCK run supplies an
// agreementId in a different shape.
func validateAgreementId(participant, agreementId string) error {
	if agreementId == "" {
		return fmt.Errorf("%w: agreementId is required", ErrInvalidAgreement)
	}

	n, err := fetchNegotiation(agreementId)
	if err != nil {
		if errors.Is(err, ErrNegotiationNotFound) {
			return fmt.Errorf("%w: %q", ErrAgreementNotFound, agreementId)
		}
		return err
	}
	if n.Participant != participant {
		return fmt.Errorf("%w: %q", ErrAgreementNotFound, agreementId)
	}
	if n.State != "FINALIZED" {
		return fmt.Errorf("%w: negotiation %q is %q, not FINALIZED", ErrInvalidAgreement, agreementId, n.State)
	}
	return nil
}

// decodeTransferRequest decodes body into a transferRequestMessage and
// checks the fields the Transfer Request Message needs per the schema.
func decodeTransferRequest(r *http.Request) (*transferRequestMessage, error) {
	var msg transferRequestMessage
	if r.Body == nil {
		return nil, fmt.Errorf("%w: request body is required", ErrInvalidAgreement)
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: request body is required", ErrInvalidAgreement)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidAgreement, err)
	}
	if msg.ConsumerPid == "" {
		return nil, fmt.Errorf("%w: consumerPid is required", ErrInvalidAgreement)
	}
	if msg.AgreementId == "" {
		return nil, fmt.Errorf("%w: agreementId is required", ErrInvalidAgreement)
	}
	if msg.Format == "" {
		return nil, fmt.Errorf("%w: format is required", ErrInvalidAgreement)
	}
	if msg.CallbackAddress == "" {
		return nil, fmt.Errorf("%w: callbackAddress is required", ErrInvalidAgreement)
	}
	return &msg, nil
}

// transferRequestInitHandler implements POST /transfers/request per the DSP
// Transfer Process HTTPS Binding: starts a new transfer in REQUESTED,
// validating agreementId against a FINALIZED negotiation before creating
// anything in transfer-process-service.
func transferRequestInitHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	msg, err := decodeTransferRequest(r)
	if err != nil {
		writeTransferError(w, http.StatusBadRequest, "", "", "invalid-request", err.Error())
		return
	}

	if err := validateAgreementId(participant, msg.AgreementId); err != nil {
		if errors.Is(err, ErrAgreementNotFound) || errors.Is(err, ErrInvalidAgreement) {
			logger.Sugar().Infow("Transfer request denied: invalid agreement", "participant", participant, "error", err)
			writeTransferError(w, http.StatusBadRequest, "", msg.ConsumerPid, "invalid-agreement", err.Error())
			return
		}
		logger.Sugar().Errorw("negotiation-service request failed", "participant", participant, "error", err)
		writeTransferError(w, http.StatusBadGateway, "", msg.ConsumerPid, "upstream-error", "Failed to validate agreement.")
		return
	}

	t, err := createTransfer(msg.ConsumerPid, participant, msg.AgreementId, msg.Format, msg.CallbackAddress, msg.DataAddress)
	if err != nil {
		mapTransferServiceError(w, "", msg.ConsumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusCreated, t)
}

// transferGetHandler implements GET /transfers/:providerPid.
func transferGetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	t, err := fetchTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, "", err)
		return
	}
	if err := checkTransferOwnership(t, participant); err != nil {
		mapTransferServiceError(w, providerPid, "", err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferStartHandler implements POST /transfers/:providerPid/start.
//
// The DSP Transfer Start Message goes both ways: the Provider sends it
// outbound to start the transfer (T3.2's job-trigger path, delivered
// straight to the Consumer's callback address - never through this inbound
// route), and the Consumer sends it inbound, either to start a transfer
// DYNAMOS itself has no job to trigger for (T3.4, DSP TCK TP-group
// validation - a plain external consumer with no DYNAMOS job spec) or to
// resume one already SUSPENDED. This handler accepts both inbound cases
// and lets transfer-process-service's own transition rules (REQUESTED or
// SUSPENDED -> STARTED) decide what is valid; anything else comes back as
// a 400 invalid-transition.
func transferStartHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferStartMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
			return
		}
	}
	if msg.ConsumerPid == "" {
		writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", "consumerPid is required")
		return
	}

	existing, err := fetchTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if err := checkTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if msg.ConsumerPid != existing.ConsumerPid {
		writeTransferError(w, http.StatusBadRequest, providerPid, msg.ConsumerPid, "invalid-request", "consumerPid does not match this transfer.")
		return
	}

	t, err := startTransfer(providerPid, msg.DataAddress)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferCompletionHandler implements
// POST /transfers/:providerPid/completion.
func transferCompletionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferCompletionMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
			return
		}
	}
	if msg.ConsumerPid == "" {
		writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", "consumerPid is required")
		return
	}

	existing, err := fetchTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if err := checkTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if msg.ConsumerPid != existing.ConsumerPid {
		writeTransferError(w, http.StatusBadRequest, providerPid, msg.ConsumerPid, "invalid-request", "consumerPid does not match this transfer.")
		return
	}

	t, err := completeTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferSuspensionHandler implements
// POST /transfers/:providerPid/suspension. code and reason are logged only.
func transferSuspensionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferSuspensionMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
			return
		}
	}
	if msg.ConsumerPid == "" {
		writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", "consumerPid is required")
		return
	}
	if msg.Code != "" || len(msg.Reason) > 0 {
		logger.Sugar().Infow("Transfer suspension requested", "providerPid", providerPid, "code", msg.Code, "reason", msg.Reason)
	}

	existing, err := fetchTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if err := checkTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if msg.ConsumerPid != existing.ConsumerPid {
		writeTransferError(w, http.StatusBadRequest, providerPid, msg.ConsumerPid, "invalid-request", "consumerPid does not match this transfer.")
		return
	}

	t, err := suspendTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferTerminationHandler implements
// POST /transfers/:providerPid/termination. code and reason are logged
// only, same as transferSuspensionHandler.
func transferTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	providerPid := r.PathValue("providerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, providerPid, "", "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferTerminationMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", err.Error())
			return
		}
	}
	if msg.ConsumerPid == "" {
		writeTransferError(w, http.StatusBadRequest, providerPid, "", "invalid-request", "consumerPid is required")
		return
	}
	if msg.Code != "" || len(msg.Reason) > 0 {
		logger.Sugar().Infow("Transfer termination requested", "providerPid", providerPid, "code", msg.Code, "reason", msg.Reason)
	}

	existing, err := fetchTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if err := checkTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}
	if msg.ConsumerPid != existing.ConsumerPid {
		writeTransferError(w, http.StatusBadRequest, providerPid, msg.ConsumerPid, "invalid-request", "consumerPid does not match this transfer.")
		return
	}

	t, err := terminateTransfer(providerPid)
	if err != nil {
		mapTransferServiceError(w, providerPid, msg.ConsumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

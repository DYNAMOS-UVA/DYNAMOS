package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
)

// transferConsumerStartMessage mirrors the DSP TransferStartMessage shape
// received on the Consumer callback - unlike transferStartMessage
// (Provider-role inbound, transfer_handler.go), providerPid is decoded
// here: the Consumer callback has no {providerPid} path segment to drive
// the lookup from, only {consumerPid}.
type transferConsumerStartMessage struct {
	ProviderPid string          `json:"providerPid"`
	ConsumerPid string          `json:"consumerPid"`
	DataAddress json.RawMessage `json:"dataAddress,omitempty"`
}

// transferConsumerCompletionMessage mirrors the DSP
// TransferCompletionMessage shape received on the Consumer callback.
type transferConsumerCompletionMessage struct {
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
}

// transferConsumerSuspensionMessage mirrors the DSP
// TransferSuspensionMessage shape received on the Consumer callback. Code
// and reason are accepted, so a well-formed message round-trips, but only
// logged - same as the Provider-role transferSuspensionMessage.
type transferConsumerSuspensionMessage struct {
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code,omitempty"`
	Reason      []interface{} `json:"reason,omitempty"`
}

// transferConsumerTerminationMessage mirrors the DSP
// TransferTerminationMessage shape received on the Consumer callback.
type transferConsumerTerminationMessage struct {
	ProviderPid string        `json:"providerPid"`
	ConsumerPid string        `json:"consumerPid"`
	Code        string        `json:"code,omitempty"`
	Reason      []interface{} `json:"reason,omitempty"`
}

// transferConsumerStatus is transferConsumerGetHandler's own response
// shape - not a DSP TransferProcess ack (see writeTransferAck): the real
// DSP TransferProcess schema has no dataAddress field, by design, data
// delivery is push-only via TransferStartMessage, never pollable through a
// GET. This is DYNAMOS's own status/data poll, the transfer-side
// counterpart to negotiationConsumerGetHandler's TCK verification poll -
// for issue #93 it is what replaces the 2026-08-06 session's throwaway
// echo-listener pod's own /latest endpoint: the real way to retrieve
// pushed data once dsp-connector can receive it itself.
type transferConsumerStatus struct {
	Context     []interface{}   `json:"@context"`
	Type        string          `json:"@type"`
	ProviderPid string          `json:"providerPid"`
	ConsumerPid string          `json:"consumerPid"`
	State       string          `json:"state"`
	DataAddress json.RawMessage `json:"dataAddress,omitempty"`
}

func writeTransferConsumerStatus(w http.ResponseWriter, status int, t *transferRecord) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(transferConsumerStatus{
		Context:     catalog.Context,
		Type:        "TransferProcess",
		ProviderPid: t.ProviderPid,
		ConsumerPid: t.ConsumerPid,
		State:       t.State,
		DataAddress: t.DataAddress,
	})
}

// decodeTransferConsumerCallbackBody decodes r's body into v, mapping a
// missing body to the same "request body is required" error every handler
// in this file uses. Mirrors decodeConsumerCallbackBody
// (negotiation_consumer_handler.go); a separate copy rather than a shared
// helper because the two packages' error sentinels
// (errMissingRequestBody vs this file's own use of it) already live in the
// negotiation file - reusing it directly avoids a needless second sentinel
// with an identical message.
func decodeTransferConsumerCallbackBody(r *http.Request, v any) error {
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

// transferConsumerGetHandler implements
// GET /:callback/transfers/:consumerPid - not a DSP Consumer Path Binding
// itself, DYNAMOS's own status/data poll (see transferConsumerStatus's own
// doc). Same ownership check as every other Consumer-role handler here.
func transferConsumerGetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	t, err := fetchConsumerTransfer(consumerPid)
	if err != nil {
		mapTransferServiceError(w, "", consumerPid, err)
		return
	}
	if err := checkConsumerTransferOwnership(t, participant); err != nil {
		mapTransferServiceError(w, t.ProviderPid, consumerPid, err)
		return
	}

	writeTransferConsumerStatus(w, http.StatusOK, t)
}

// transferConsumerStartHandler implements
// POST /:callback/transfers/:consumerPid/start (Transfer Start Message)
// per the DSP HTTPS binding's Consumer Path Bindings. This is the normal
// happy path for issue #93's demo: the Provider-initiated push carrying
// the real computed data, the exact message a throwaway echo-listener pod
// had to stand in for before this handler existed (see
// transferConsumerStatus's own doc).
func transferConsumerStartHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferConsumerStartMessage
	if err := decodeTransferConsumerCallbackBody(r, &msg); err != nil {
		writeTransferError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
		return
	}

	existing, err := fetchConsumerTransfer(consumerPid)
	if err != nil {
		mapTransferServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeTransferError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this transfer.")
		return
	}

	t, err := recordConsumerTransferStart(consumerPid, msg.DataAddress)
	if err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferConsumerCompletionHandler implements
// POST /:callback/transfers/:consumerPid/completion (Transfer Completion
// Message) per the Consumer Path Bindings.
func transferConsumerCompletionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferConsumerCompletionMessage
	if err := decodeTransferConsumerCallbackBody(r, &msg); err != nil {
		writeTransferError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
		return
	}

	existing, err := fetchConsumerTransfer(consumerPid)
	if err != nil {
		mapTransferServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeTransferError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this transfer.")
		return
	}

	t, err := recordConsumerTransferCompletion(consumerPid)
	if err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferConsumerSuspensionHandler implements
// POST /:callback/transfers/:consumerPid/suspension (Transfer Suspension
// Message) per the Consumer Path Bindings.
func transferConsumerSuspensionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferConsumerSuspensionMessage
	if err := decodeTransferConsumerCallbackBody(r, &msg); err != nil {
		writeTransferError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
		return
	}
	if msg.Code != "" || len(msg.Reason) > 0 {
		logger.Sugar().Infow("Transfer suspension received", "consumerPid", consumerPid, "code", msg.Code, "reason", msg.Reason)
	}

	existing, err := fetchConsumerTransfer(consumerPid)
	if err != nil {
		mapTransferServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeTransferError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this transfer.")
		return
	}

	t, err := recordConsumerTransferSuspension(consumerPid)
	if err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

// transferConsumerTerminationHandler implements
// POST /:callback/transfers/:consumerPid/termination (Transfer
// Termination Message) per the Consumer Path Bindings.
func transferConsumerTerminationHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	consumerPid := r.PathValue("consumerPid")

	participant, ok := participantFromRequest(r)
	if !ok {
		writeTransferError(w, http.StatusUnauthorized, "", consumerPid, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	var msg transferConsumerTerminationMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil && !errors.Is(err, io.EOF) {
			writeTransferError(w, http.StatusBadRequest, "", consumerPid, "invalid-request", err.Error())
			return
		}
	}
	if msg.Code != "" || len(msg.Reason) > 0 {
		logger.Sugar().Infow("Transfer termination received", "consumerPid", consumerPid, "code", msg.Code, "reason", msg.Reason)
	}

	existing, err := fetchConsumerTransfer(consumerPid)
	if err != nil {
		mapTransferServiceError(w, msg.ProviderPid, consumerPid, err)
		return
	}
	if err := checkConsumerTransferOwnership(existing, participant); err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}
	if msg.ProviderPid != "" && msg.ProviderPid != existing.ProviderPid {
		writeTransferError(w, http.StatusBadRequest, existing.ProviderPid, consumerPid, "invalid-request", "providerPid does not match this transfer.")
		return
	}

	t, err := recordConsumerTransferTermination(consumerPid)
	if err != nil {
		mapTransferServiceError(w, existing.ProviderPid, consumerPid, err)
		return
	}

	writeTransferAck(w, http.StatusOK, t)
}

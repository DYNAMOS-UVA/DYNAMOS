package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ErrTransferNotFound and ErrTransferInvalidTransition are sentinels.
// Handlers use them to map transfer-process-service's internal-API error
// codes to the right DSP-level HTTP response.
var (
	ErrTransferNotFound          = errors.New("transfer-process-service: transfer not found")
	ErrTransferInvalidTransition = errors.New("transfer-process-service: invalid state transition")
)

// ErrTransferForbidden signals that the authenticated participant does not
// own this transfer. mapTransferServiceError maps it to the same 404
// response as ErrTransferNotFound. A non-owner must not be able to tell
// "does not exist" apart from "exists, but is not yours".
var ErrTransferForbidden = errors.New("transfer-process-service: participant does not own this transfer")

// transferRecord is the subset of transfer-process-service's TransferProcess
// JSON this connector needs. Timestamps stay internal to
// transfer-process-service. RemoteParticipant and DataAddress are only ever
// populated on a Consumer-role record (issue #93) - a Provider-role record
// leaves both zero, and writeTransferAck never emits either, so decoding
// them here changes nothing about the existing Provider-role DSP ack.
// DataAddress exists so transferConsumerGetHandler can expose the real
// pushed data (see writeTransferConsumerStatus in
// transfer_consumer_handler.go) - the DSP TransferProcess ack itself never
// carries dataAddress, by spec, so this never reaches writeTransferAck's
// strict shape.
type transferRecord struct {
	ProviderPid       string          `json:"providerPid"`
	ConsumerPid       string          `json:"consumerPid"`
	Participant       string          `json:"participant"`
	RemoteParticipant string          `json:"remoteParticipant,omitempty"`
	AgreementId       string          `json:"agreementId"`
	State             string          `json:"state"`
	DataAddress       json.RawMessage `json:"dataAddress,omitempty"`
}

// checkConsumerTransferOwnership reports ErrTransferForbidden if
// participant isn't the remote Provider DYNAMOS declared when it initiated
// this transfer (issue #93's create-as-consumer). The Consumer-role
// counterpart to checkTransferOwnership - compared against
// RemoteParticipant instead of Participant, same reasoning
// checkConsumerNegotiationOwnership documents.
func checkConsumerTransferOwnership(t *transferRecord, participant string) error {
	if t.RemoteParticipant != participant {
		return ErrTransferForbidden
	}
	return nil
}

// checkTransferOwnership reports ErrTransferForbidden if participant did
// not open t. Every provider endpoint but the initiating request must call
// this on the fetched record before it acts on that record. Without this
// check, any authenticated participant could read or drive someone else's
// transfer, just by knowing its providerPid.
func checkTransferOwnership(t *transferRecord, participant string) error {
	if t.Participant != participant {
		return ErrTransferForbidden
	}
	return nil
}

var transferServiceClient = &http.Client{Timeout: 5 * time.Second}

// transferServiceErrorCodes maps transfer-process-service's internal-API
// error codes (go/cmd/transfer-process-service/transfer_handler.go) to this
// package's sentinels.
var transferServiceErrorCodes = map[string]error{
	"transfer-not-found": ErrTransferNotFound,
	"invalid-transition": ErrTransferInvalidTransition,
}

// transferServiceStatusFallback: if transfer-process-service's error body
// ever fails to decode, for example an etcd timeout mid-response, this
// falls back on the HTTP status it still sent. A mangled body should not
// downgrade a real 404 or 409 into a generic upstream failure.
var transferServiceStatusFallback = map[int]error{
	http.StatusNotFound: ErrTransferNotFound,
	http.StatusConflict: ErrTransferInvalidTransition,
}

// transferErrorFromResponse maps a non-2xx transfer-process-service
// response to a sentinel error. For anything unexpected, for example an
// etcd I/O failure on that service's side, it returns a generic wrapped
// error instead.
func transferErrorFromResponse(resp *http.Response) error {
	return mapInternalServiceError("transfer-process-service", resp, transferServiceErrorCodes, transferServiceStatusFallback)
}

// postTransfer POSTs body (JSON-encoded, may be nil) to path on
// transfer-process-service, and decodes a transferRecord from a 200 or 201
// response. Anything else becomes an error.
func postTransfer(path string, body any) (*transferRecord, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, fmt.Errorf("encoding transfer-process-service request: %w", err)
		}
	}

	resp, err := transferServiceClient.Post(transferServiceURL+path, "application/json", &buf)
	if err != nil {
		return nil, fmt.Errorf("calling transfer-process-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, transferErrorFromResponse(resp)
	}

	var t transferRecord
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decoding transfer-process-service response: %w", err)
	}
	return &t, nil
}

// fetchTransfer calls transfer-process-service's
// GET /internal/v1/transfers/{id}, backing the DSP
// GET /transfers/:providerPid endpoint.
func fetchTransfer(providerPid string) (*transferRecord, error) {
	reqURL := transferServiceURL + "/internal/v1/transfers/" + url.PathEscape(providerPid)
	resp, err := transferServiceClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("calling transfer-process-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, transferErrorFromResponse(resp)
	}

	var t transferRecord
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decoding transfer-process-service response: %w", err)
	}
	return &t, nil
}

// createTransfer calls transfer-process-service's
// POST /internal/v1/transfers, the initiating Transfer Request Message
// (no providerPid yet), backing DSP POST /transfers/request.
func createTransfer(consumerPid, participant, agreementId, format, callbackAddress string, dataAddress json.RawMessage) (*transferRecord, error) {
	body := map[string]any{
		"consumerPid":     consumerPid,
		"participant":     participant,
		"agreementId":     agreementId,
		"format":          format,
		"callbackAddress": callbackAddress,
	}
	// Omit the key entirely when dataAddress is empty. A present key with a
	// nil json.RawMessage value marshals to the JSON literal null, not to a
	// missing field. transfer-process-service would then store the text
	// "null" as this transfer's DataAddress, instead of leaving it unset.
	if len(dataAddress) > 0 {
		body["dataAddress"] = dataAddress
	}
	return postTransfer("/internal/v1/transfers", body)
}

// startTransfer calls transfer-process-service's
// POST /internal/v1/transfers/{id}/start, backing DSP
// POST /transfers/:providerPid/start - every inbound, Consumer-initiated
// Start message, whether it starts a transfer from REQUESTED or resumes
// one from SUSPENDED. See transferStartHandler's own doc comment.
func startTransfer(providerPid string, dataAddress json.RawMessage) (*transferRecord, error) {
	body := map[string]any{}
	// Same reasoning as createTransfer: omit the key rather than send a
	// present-but-null value, so a resume call with no new dataAddress
	// leaves the transfer's stored DataAddress untouched.
	if len(dataAddress) > 0 {
		body["dataAddress"] = dataAddress
	}
	return postTransfer("/internal/v1/transfers/"+url.PathEscape(providerPid)+"/start", body)
}

// completeTransfer calls transfer-process-service's
// POST /internal/v1/transfers/{id}/completion, backing DSP
// POST /transfers/:providerPid/completion.
func completeTransfer(providerPid string) (*transferRecord, error) {
	return postTransfer("/internal/v1/transfers/"+url.PathEscape(providerPid)+"/completion", struct{}{})
}

// suspendTransfer calls transfer-process-service's
// POST /internal/v1/transfers/{id}/suspension, backing DSP
// POST /transfers/:providerPid/suspension.
func suspendTransfer(providerPid string) (*transferRecord, error) {
	return postTransfer("/internal/v1/transfers/"+url.PathEscape(providerPid)+"/suspension", struct{}{})
}

// terminateTransfer calls transfer-process-service's
// POST /internal/v1/transfers/{id}/termination, backing DSP
// POST /transfers/:providerPid/termination.
func terminateTransfer(providerPid string) (*transferRecord, error) {
	return postTransfer("/internal/v1/transfers/"+url.PathEscape(providerPid)+"/termination", struct{}{})
}

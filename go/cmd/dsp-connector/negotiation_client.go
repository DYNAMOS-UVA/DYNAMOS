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

// ErrNegotiationNotFound / ErrNegotiationInvalidTransition: sentinels so
// handlers can map negotiation-service's internal-API error codes to the
// right DSP-level HTTP response.
var (
	ErrNegotiationNotFound          = errors.New("negotiation-service: negotiation not found")
	ErrNegotiationInvalidTransition = errors.New("negotiation-service: invalid state transition")
)

// negotiationServiceErrorResponse mirrors negotiation-service's own
// internalError shape (go/cmd/negotiation-service/negotiation_handler.go).
type negotiationServiceErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// negotiationRecord is the subset of negotiation-service's Negotiation JSON
// this connector actually needs to build a DSP ContractNegotiation ack -
// Party/Offer/Agreement/timestamps stay internal to negotiation-service.
type negotiationRecord struct {
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
	State       string `json:"state"`
}

var negotiationServiceClient = &http.Client{Timeout: 5 * time.Second}

// negotiationErrorFromResponse maps a non-2xx negotiation-service response to
// a sentinel error, or a generic wrapped error for anything unexpected (etcd
// I/O failures on negotiation-service's side, network errors, etc) - same
// shape as catalog_client.go's errorFromResponse.
func negotiationErrorFromResponse(resp *http.Response) error {
	var ne negotiationServiceErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&ne); err != nil {
		return fmt.Errorf("negotiation-service returned %d with unparseable body: %w", resp.StatusCode, err)
	}

	switch ne.Code {
	case "negotiation-not-found":
		return ErrNegotiationNotFound
	case "invalid-transition":
		return ErrNegotiationInvalidTransition
	default:
		return fmt.Errorf("negotiation-service returned %d (%s): %s", resp.StatusCode, ne.Code, ne.Error)
	}
}

// postNegotiation POSTs body (JSON-encoded, may be nil) to path on
// negotiation-service and decodes a negotiationRecord from a 200/201
// response, or an error from anything else.
func postNegotiation(path string, body any) (*negotiationRecord, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, fmt.Errorf("encoding negotiation-service request: %w", err)
		}
	}

	resp, err := negotiationServiceClient.Post(negotiationServiceURL+path, "application/json", &buf)
	if err != nil {
		return nil, fmt.Errorf("calling negotiation-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, negotiationErrorFromResponse(resp)
	}

	var n negotiationRecord
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return nil, fmt.Errorf("decoding negotiation-service response: %w", err)
	}
	return &n, nil
}

// fetchNegotiation calls negotiation-service's GET /internal/v1/negotiations/{id},
// backing the DSP GET /negotiations/:providerPid endpoint.
func fetchNegotiation(providerPid string) (*negotiationRecord, error) {
	reqURL := negotiationServiceURL + "/internal/v1/negotiations/" + url.PathEscape(providerPid)
	resp, err := negotiationServiceClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("calling negotiation-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, negotiationErrorFromResponse(resp)
	}

	var n negotiationRecord
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return nil, fmt.Errorf("decoding negotiation-service response: %w", err)
	}
	return &n, nil
}

// createNegotiation calls negotiation-service's
// POST /internal/v1/negotiations - the initiating Contract Request Message
// (no providerPid yet), backing DSP POST /negotiations/request.
func createNegotiation(consumerPid string, offer json.RawMessage) (*negotiationRecord, error) {
	return postNegotiation("/internal/v1/negotiations", map[string]any{
		"consumerPid": consumerPid,
		"offer":       offer,
	})
}

// counterRequestNegotiation calls negotiation-service's
// POST /internal/v1/negotiations/{id}/request - a counter-request, backing
// DSP POST /negotiations/:providerPid/request.
func counterRequestNegotiation(providerPid string, offer json.RawMessage) (*negotiationRecord, error) {
	return postNegotiation("/internal/v1/negotiations/"+url.PathEscape(providerPid)+"/request", map[string]any{
		"offer": offer,
	})
}

// acceptNegotiation calls negotiation-service's
// POST /internal/v1/negotiations/{id}/events with eventType ACCEPTED,
// backing DSP POST /negotiations/:providerPid/events.
func acceptNegotiation(providerPid string) (*negotiationRecord, error) {
	return postNegotiation("/internal/v1/negotiations/"+url.PathEscape(providerPid)+"/events", map[string]string{
		"eventType": "ACCEPTED",
	})
}

// verifyNegotiationAgreement calls negotiation-service's
// POST /internal/v1/negotiations/{id}/agreement/verification, backing DSP
// POST /negotiations/:providerPid/agreement/verification.
func verifyNegotiationAgreement(providerPid string) (*negotiationRecord, error) {
	return postNegotiation("/internal/v1/negotiations/"+url.PathEscape(providerPid)+"/agreement/verification", struct{}{})
}

// terminateNegotiation calls negotiation-service's
// POST /internal/v1/negotiations/{id}/termination, backing DSP
// POST /negotiations/:providerPid/termination.
func terminateNegotiation(providerPid string) (*negotiationRecord, error) {
	return postNegotiation("/internal/v1/negotiations/"+url.PathEscape(providerPid)+"/termination", struct{}{})
}

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

// ErrNegotiationForbidden signals that the authenticated participant is not
// the one who owns this negotiation. mapNegotiationServiceError maps it to
// the exact same 404 response as ErrNegotiationNotFound - a non-owner must
// not be able to tell "doesn't exist" apart from "exists, but isn't yours".
var ErrNegotiationForbidden = errors.New("negotiation-service: participant does not own this negotiation")

// negotiationRecord is the subset of negotiation-service's Negotiation JSON
// this connector actually needs to build a DSP ContractNegotiation ack -
// Offer/Agreement/timestamps stay internal to negotiation-service.
type negotiationRecord struct {
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
	Participant string `json:"participant"`
	State       string `json:"state"`
}

// checkNegotiationOwnership reports ErrNegotiationForbidden if participant
// isn't the one who opened n - every provider endpoint but the initiating
// request must call this on the fetched/looked-up record before acting on
// it, or any authenticated participant could read or drive someone else's
// negotiation just by knowing its providerPid.
func checkNegotiationOwnership(n *negotiationRecord, participant string) error {
	if n.Participant != participant {
		return ErrNegotiationForbidden
	}
	return nil
}

var negotiationServiceClient = &http.Client{Timeout: 5 * time.Second}

// negotiationServiceErrorCodes maps negotiation-service's internal-API error
// codes (go/cmd/negotiation-service/negotiation_handler.go) to this
// package's sentinels.
var negotiationServiceErrorCodes = map[string]error{
	"negotiation-not-found": ErrNegotiationNotFound,
	"invalid-transition":    ErrNegotiationInvalidTransition,
}

// negotiationServiceStatusFallback: if negotiation-service's error body ever
// fails to decode (a proxy/gateway substituting its own error page, an etcd
// timeout mid-response, etc), fall back on the HTTP status it still sent
// rather than treating every unparseable body the same as an opaque 502 -
// a genuine 404/409 shouldn't get miscategorized as "upstream-error" just
// because its body wasn't the {code,error} shape.
var negotiationServiceStatusFallback = map[int]error{
	http.StatusNotFound: ErrNegotiationNotFound,
	http.StatusConflict: ErrNegotiationInvalidTransition,
}

// negotiationErrorFromResponse maps a non-2xx negotiation-service response to
// a sentinel error, or a generic wrapped error for anything unexpected (etcd
// I/O failures on negotiation-service's side, network errors, etc).
func negotiationErrorFromResponse(resp *http.Response) error {
	return mapInternalServiceError("negotiation-service", resp, negotiationServiceErrorCodes, negotiationServiceStatusFallback)
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
func createNegotiation(consumerPid, participant, callbackAddress string, offer json.RawMessage) (*negotiationRecord, error) {
	return postNegotiation("/internal/v1/negotiations", map[string]any{
		"consumerPid":     consumerPid,
		"participant":     participant,
		"callbackAddress": callbackAddress,
		"offer":           offer,
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

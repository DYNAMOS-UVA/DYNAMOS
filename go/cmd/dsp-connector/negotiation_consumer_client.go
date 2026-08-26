package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// consumerNegotiationRecord is the subset of negotiation-service's
// Consumer-role Negotiation JSON this connector needs to terminate the
// Consumer Path Bindings (#81) - mirrors negotiationRecord's shape, plus
// RemoteParticipant (the Provider-role equivalent has no such field: a
// Provider-role negotiation's Participant already *is* the other side's
// identity, see negotiation-service's own Negotiation.Participant doc).
type consumerNegotiationRecord struct {
	ProviderPid       string `json:"providerPid"`
	ConsumerPid       string `json:"consumerPid"`
	RemoteParticipant string `json:"remoteParticipant"`
	// ProviderEndpoint was not needed until issue #93's transfer-consumer
	// initiate handler: it looks up a FINALIZED negotiation to learn who
	// the remote Provider is and where to send the outbound Transfer
	// Request Message, instead of re-deriving either from the initiate
	// caller (see transfer_consumer_initiate_handler.go). agreementId
	// itself is n.ProviderPid, per dsp-connector's own established
	// convention (validateAgreementId in transfer_handler.go) - the
	// Agreement object's own ODRL @id is never needed here.
	ProviderEndpoint string `json:"providerEndpoint,omitempty"`
	State            string `json:"state"`
}

// checkConsumerNegotiationOwnership reports ErrNegotiationForbidden if
// participant isn't the remote Provider DYNAMOS declared when it initiated
// this negotiation (#80's create-as-consumer). The Consumer-role
// counterpart to checkNegotiationOwnership - same IDOR protection, compared
// against RemoteParticipant instead of Participant since a Consumer-role
// negotiation's "who owns this" question is "which Provider is DYNAMOS
// negotiating with", not "which Consumer opened it".
func checkConsumerNegotiationOwnership(n *consumerNegotiationRecord, participant string) error {
	if n.RemoteParticipant != participant {
		return ErrNegotiationForbidden
	}
	return nil
}

// postConsumerNegotiation POSTs body (JSON-encoded, may be nil) to path on
// negotiation-service and decodes a consumerNegotiationRecord from a
// 200/201 response, or an error from anything else - the Consumer-role
// counterpart to postNegotiation (negotiation_client.go).
func postConsumerNegotiation(path string, body any) (*consumerNegotiationRecord, error) {
	resp, err := postToNegotiationService(path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, negotiationErrorFromResponse(resp)
	}

	var n consumerNegotiationRecord
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return nil, fmt.Errorf("decoding negotiation-service response: %w", err)
	}
	return &n, nil
}

// fetchConsumerNegotiation calls negotiation-service's
// GET /internal/v1/negotiations/consumer/{id}, the Consumer-role
// counterpart to fetchNegotiation.
func fetchConsumerNegotiation(consumerPid string) (*consumerNegotiationRecord, error) {
	reqURL := negotiationServiceURL + "/internal/v1/negotiations/consumer/" + url.PathEscape(consumerPid)
	resp, err := negotiationServiceClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("calling negotiation-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, negotiationErrorFromResponse(resp)
	}

	var n consumerNegotiationRecord
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return nil, fmt.Errorf("decoding negotiation-service response: %w", err)
	}
	return &n, nil
}

// createConsumerNegotiation calls negotiation-service's
// POST /internal/v1/negotiations/consumer - the initiating Contract Request
// Message DYNAMOS itself sends as Consumer (#80), backing #83's
// negotiationConsumerInitiateHandler.
func createConsumerNegotiation(providerEndpoint, participant, remoteParticipant, callbackAddress string, offer json.RawMessage) (*consumerNegotiationRecord, error) {
	return postConsumerNegotiation("/internal/v1/negotiations/consumer", map[string]any{
		"providerEndpoint":  providerEndpoint,
		"participant":       participant,
		"remoteParticipant": remoteParticipant,
		"callbackAddress":   callbackAddress,
		"offer":             offer,
	})
}

// recordConsumerOffer calls negotiation-service's
// POST /internal/v1/negotiations/consumer/{id}/offer, backing the DSP
// POST /:callback/negotiations/:consumerPid/offers Consumer Path Binding.
func recordConsumerOffer(consumerPid string, offer json.RawMessage) (*consumerNegotiationRecord, error) {
	return postConsumerNegotiation("/internal/v1/negotiations/consumer/"+url.PathEscape(consumerPid)+"/offer", map[string]any{
		"offer": offer,
	})
}

// recordConsumerAgreement calls negotiation-service's
// POST /internal/v1/negotiations/consumer/{id}/agreement, backing the DSP
// POST /:callback/negotiations/:consumerPid/agreement Consumer Path Binding.
func recordConsumerAgreement(consumerPid string, agreement json.RawMessage) (*consumerNegotiationRecord, error) {
	return postConsumerNegotiation("/internal/v1/negotiations/consumer/"+url.PathEscape(consumerPid)+"/agreement", map[string]any{
		"agreement": agreement,
	})
}

// recordConsumerFinalized calls negotiation-service's
// POST /internal/v1/negotiations/consumer/{id}/events with eventType
// FINALIZED, backing the DSP
// POST /:callback/negotiations/:consumerPid/events Consumer Path Binding.
func recordConsumerFinalized(consumerPid string) (*consumerNegotiationRecord, error) {
	return postConsumerNegotiation("/internal/v1/negotiations/consumer/"+url.PathEscape(consumerPid)+"/events", map[string]string{
		"eventType": "FINALIZED",
	})
}

// recordConsumerTermination calls negotiation-service's
// POST /internal/v1/negotiations/consumer/{id}/termination, backing the DSP
// POST /:callback/negotiations/:consumerPid/termination Consumer Path
// Binding.
func recordConsumerTermination(consumerPid string) (*consumerNegotiationRecord, error) {
	return postConsumerNegotiation("/internal/v1/negotiations/consumer/"+url.PathEscape(consumerPid)+"/termination", struct{}{})
}

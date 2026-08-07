package main

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// createConsumerTransfer calls transfer-process-service's
// POST /internal/v1/transfers/consumer - the initiating Transfer Request
// Message DYNAMOS itself sends as Consumer (issue #93), backing
// transferConsumerInitiateHandler. Reuses postTransfer/transferRecord from
// transfer_client.go: the ack shape dsp-connector needs back (providerPid/
// consumerPid/state) is identical for the Provider- and Consumer-role
// create calls.
func createConsumerTransfer(providerEndpoint, participant, remoteParticipant, agreementId, format, callbackAddress string, dataAddress json.RawMessage) (*transferRecord, error) {
	body := map[string]any{
		"providerEndpoint":  providerEndpoint,
		"participant":       participant,
		"remoteParticipant": remoteParticipant,
		"agreementId":       agreementId,
		"format":            format,
		"callbackAddress":   callbackAddress,
	}
	// Same reasoning as createTransfer (transfer_client.go): omit the key
	// entirely rather than send a present-but-null value.
	if len(dataAddress) > 0 {
		body["dataAddress"] = dataAddress
	}
	return postTransfer("/internal/v1/transfers/consumer", body)
}

// fetchConsumerTransfer calls transfer-process-service's
// GET /internal/v1/transfers/consumer/{id}, the Consumer-role counterpart
// to fetchTransfer.
func fetchConsumerTransfer(consumerPid string) (*transferRecord, error) {
	reqURL := transferServiceURL + "/internal/v1/transfers/consumer/" + url.PathEscape(consumerPid)
	resp, err := transferServiceClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("calling transfer-process-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, transferErrorFromResponse(resp)
	}

	var t transferRecord
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decoding transfer-process-service response: %w", err)
	}
	return &t, nil
}

// recordConsumerTransferStart calls transfer-process-service's
// POST /internal/v1/transfers/consumer/{id}/start, backing the DSP
// POST /:callback/transfers/:consumerPid/start Consumer Path Binding.
func recordConsumerTransferStart(consumerPid string, dataAddress json.RawMessage) (*transferRecord, error) {
	body := map[string]any{}
	if len(dataAddress) > 0 {
		body["dataAddress"] = dataAddress
	}
	return postTransfer("/internal/v1/transfers/consumer/"+url.PathEscape(consumerPid)+"/start", body)
}

// recordConsumerTransferCompletion calls transfer-process-service's
// POST /internal/v1/transfers/consumer/{id}/completion, backing the DSP
// POST /:callback/transfers/:consumerPid/completion Consumer Path Binding.
func recordConsumerTransferCompletion(consumerPid string) (*transferRecord, error) {
	return postTransfer("/internal/v1/transfers/consumer/"+url.PathEscape(consumerPid)+"/completion", struct{}{})
}

// recordConsumerTransferSuspension calls transfer-process-service's
// POST /internal/v1/transfers/consumer/{id}/suspension, backing the DSP
// POST /:callback/transfers/:consumerPid/suspension Consumer Path Binding.
func recordConsumerTransferSuspension(consumerPid string) (*transferRecord, error) {
	return postTransfer("/internal/v1/transfers/consumer/"+url.PathEscape(consumerPid)+"/suspension", struct{}{})
}

// recordConsumerTransferTermination calls transfer-process-service's
// POST /internal/v1/transfers/consumer/{id}/termination, backing the DSP
// POST /:callback/transfers/:consumerPid/termination Consumer Path Binding.
func recordConsumerTransferTermination(consumerPid string) (*transferRecord, error) {
	return postTransfer("/internal/v1/transfers/consumer/"+url.PathEscape(consumerPid)+"/termination", struct{}{})
}

package main

import "encoding/json"

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

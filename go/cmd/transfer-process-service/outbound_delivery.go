package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// dspContext matches pkg/catalog.Context. transfer-process-service does
// not depend on that package. This file repeats the constant instead of
// adding a dependency for one value. This follows the same convention as
// negotiation-service's own outbound_delivery.go.
var dspContext = []string{"https://w3id.org/dspace/2025/1/context.jsonld"}

// deliverToConsumer sends a provider-initiated DSP message to the
// transfer's stored CallbackAddress. The DSP HTTPS binding's Consumer
// Callback Path Bindings set the target URL:
// CallbackAddress+"/transfers/"+ConsumerPid+"/"+path (see
// transfer.process.binding.https.md).
//
// This function exists from T3.1.3 onward. It is not a later bug fix.
// negotiation-service shipped T2.2 with no outbound delivery. T2.5's TCK
// pass added that fix later. Transfer process needs outbound delivery
// more: the Provider-initiated Start is the normal happy path here, not
// an edge case (see docs/transfer/dsp-transfer-state-machine.md).
//
// Delivery is best-effort. A delivery failure is only logged. It does not
// fail the internal API call that triggered it. The state transition
// already happened and is already persisted - that record is
// transfer-process-service's own source of truth. The DSP spec itself
// treats delivery as an async push, with no synchronous link back to the
// triggering action. This function uses the same retry shape as
// negotiation-service's deliverToConsumer.
//
// Attaches partyDAT as the Authorization header when configured (issue
// #93 finding - see partyDAT's own doc comment): a real Consumer's
// callback handler DAT-verifies this header, unlike the DSP TCK's own
// mock consumer, which never enforced it.
func deliverToConsumer(t *TransferProcess, path string, message any) {
	if t.CallbackAddress == "" {
		logger.Sugar().Warnw("no callbackAddress stored, skipping delivery", "providerPid", t.ProviderPid, "path", path)
		return
	}

	body, err := json.Marshal(message)
	if err != nil {
		logger.Sugar().Errorw("failed to marshal outbound DSP message", "providerPid", t.ProviderPid, "path", path, "error", err)
		return
	}

	url := t.CallbackAddress + "/transfers/" + t.ConsumerPid + "/" + path
	client := http.Client{Timeout: 20 * time.Second}

	const maxAttempts = 5
	backoff := 250 * time.Millisecond
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, reqErr := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			logger.Sugar().Errorw("failed to build outbound DSP delivery request", "providerPid", t.ProviderPid, "url", url, "error", reqErr)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		// Fetched fresh per attempt - see negotiation-service's own
		// deliverToConsumer for why (a real DCP verifier single-uses each
		// token's "jti").
		authHeader := ""
		if stsTokenURL != "" && stsClientID != "" && stsClientSecret != "" {
			if token, err := fetchSTSToken(t.Participant); err != nil {
				logger.Sugar().Errorw("failed to fetch STS token for outbound delivery, falling back to partyDAT", "providerPid", t.ProviderPid, "audience", t.Participant, "attempt", attempt, "error", err)
			} else {
				authHeader = "Bearer " + token
			}
		}
		if authHeader == "" && partyDAT != "" {
			authHeader = "Bearer " + partyDAT
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		resp, err := client.Do(req)
		if err != nil {
			if attempt == maxAttempts {
				logger.Sugar().Errorw("failed to deliver outbound DSP message", "providerPid", t.ProviderPid, "url", url, "attempts", attempt, "error", err)
				return
			}
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return
		}
		if attempt == maxAttempts {
			logger.Sugar().Warnw("consumer rejected outbound DSP message", "providerPid", t.ProviderPid, "url", url, "attempts", attempt, "status", resp.StatusCode)
			return
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}

// ErrProviderRequestFailed marks any failure of the outbound Transfer
// Request Message to a remote Provider: a network error, a non-2xx
// response, or a 2xx response missing providerPid. Distinct from
// ErrTransferNotFound/ErrInvalidTransition - this is an upstream failure,
// so the internal API maps it to 502, not 404/409. Mirrors
// negotiation-service's own ErrProviderRequestFailed.
var ErrProviderRequestFailed = errors.New("outbound transfer request to provider failed")

// transferRequestAck is the minimal shape needed out of the remote
// Provider's response to an initiating Transfer Request Message - the DSP
// TransferProcess ack (transfer-process-schema.json), just the field this
// side needs to adopt the transfer (providerPid).
type transferRequestAck struct {
	ProviderPid string `json:"providerPid"`
}

// sendTransferRequest POSTs the initiating Transfer Request Message to a
// remote Provider's transfers/request endpoint, synchronously - unlike
// deliverToConsumer's fire-and-forget push, the caller
// (transfersConsumerCollectionHandler) needs the real providerPid back
// before it can persist anything durable, so this is a single attempt, no
// retry: on failure the internal-API caller can just retry the whole
// create-as-consumer call, there is nothing partial to clean up first.
// Mirrors negotiation-service's sendContractRequest.
func sendTransferRequest(providerEndpoint, participant string, t *TransferProcess) (string, error) {
	msg := map[string]any{
		"@context":        dspContext,
		"@type":           "TransferRequestMessage",
		"consumerPid":     t.ConsumerPid,
		"agreementId":     t.AgreementId,
		"format":          t.Format,
		"callbackAddress": t.CallbackAddress,
	}
	// Omit the key entirely when DataAddress is empty, same reasoning
	// dsp-connector's createTransfer/createConsumerTransfer already
	// document: a present key with a nil json.RawMessage value marshals
	// to the JSON literal null, not to a missing field - and unmarshaling
	// that literal null into a json.RawMessage on the receiving end
	// stores the 4 bytes "null" as the value, not an empty slice. A
	// job-less transfer's len(t.DataAddress)==0 guard (see
	// triggerJobAndDeliver) would then see a false non-empty DataAddress
	// and try to trigger a job with "null" as the job spec - live-found,
	// issue #93's demo: this crashed api-gateway's requestHandler with a
	// nil-map write once the "null" bytes decoded into a nil
	// map[string]interface{}.
	if len(t.DataAddress) > 0 {
		msg["dataAddress"] = t.DataAddress
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("%w: marshaling request: %w", ErrProviderRequestFailed, err)
	}

	req, err := http.NewRequest(http.MethodPost, providerEndpoint+"/transfers/request", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: building request: %w", ErrProviderRequestFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", participant)

	client := http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderRequestFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: provider returned status %d", ErrProviderRequestFailed, resp.StatusCode)
	}

	var ack transferRequestAck
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		return "", fmt.Errorf("%w: decoding provider response: %w", ErrProviderRequestFailed, err)
	}
	if ack.ProviderPid == "" {
		return "", fmt.Errorf("%w: provider response has no providerPid", ErrProviderRequestFailed)
	}

	return ack.ProviderPid, nil
}

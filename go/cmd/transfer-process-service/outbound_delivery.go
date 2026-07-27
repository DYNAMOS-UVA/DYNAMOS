package main

import (
	"bytes"
	"encoding/json"
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
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
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

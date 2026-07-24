package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

// dspContext matches pkg/catalog.Context - negotiation-service has no
// dependency on that package, so it's repeated here rather than pulled in
// just for one constant.
var dspContext = []string{"https://w3id.org/dspace/2025/1/context.jsonld"}

// deliverToConsumer POSTs a provider-initiated DSP message to the
// negotiation's stored CallbackAddress, per the DSP HTTPS binding's Consumer
// Path Bindings (contract.negotiation.binding.https.md): every such message
// goes to CallbackAddress+"/negotiations/"+ConsumerPid+"/"+path.
//
// Best-effort: a delivery failure is logged but does not fail the internal
// API call that triggered it - the state transition already happened and
// was persisted (negotiation-service's own source of truth), matching how
// the DSP spec itself treats this as an async push with no synchronous
// coupling back to the triggering action. A short retry covers a real,
// observed race (T2.5, DSP TCK): a caller reacting to a state change via a
// fast side channel (tck_auto_responder.go's etcd watch) can fire before the
// consumer's own callback listener for this specific negotiation finishes
// registering, producing a transient 404 that isn't this consumer's fault.
// Full retry/dead-lettering beyond that remains out of scope.
func deliverToConsumer(n *Negotiation, path string, message any) {
	if n.CallbackAddress == "" {
		logger.Sugar().Warnw("no callbackAddress stored, skipping delivery", "providerPid", n.ProviderPid, "path", path)
		return
	}

	body, err := json.Marshal(message)
	if err != nil {
		logger.Sugar().Errorw("failed to marshal outbound DSP message", "providerPid", n.ProviderPid, "path", path, "error", err)
		return
	}

	url := n.CallbackAddress + "/negotiations/" + n.ConsumerPid + "/" + path
	client := http.Client{Timeout: 20 * time.Second}

	const maxAttempts = 5
	backoff := 250 * time.Millisecond
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			if attempt == maxAttempts {
				logger.Sugar().Errorw("failed to deliver outbound DSP message", "providerPid", n.ProviderPid, "url", url, "attempts", attempt, "error", err)
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
			logger.Sugar().Warnw("consumer rejected outbound DSP message", "providerPid", n.ProviderPid, "url", url, "attempts", attempt, "status", resp.StatusCode)
			return
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}

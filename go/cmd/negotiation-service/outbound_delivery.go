package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
//
// Attaches partyDAT as the Authorization header when configured (issue
// #93 finding - see partyDAT's own doc comment): a real Consumer's
// callback handler DAT-verifies this header, unlike the DSP TCK's own
// mock consumer, which never enforced it.
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
		req, reqErr := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			logger.Sugar().Errorw("failed to build outbound DSP delivery request", "providerPid", n.ProviderPid, "url", url, "error", reqErr)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		// Fetched fresh per attempt, not once for the whole retry loop: a
		// real DCP verifier single-uses each token's "jti" (anti-replay),
		// so resending the same token on a retry gets rejected as an
		// expired/reused JTI even though the request itself is a genuine
		// retry, not a replay attack.
		authHeader := ""
		if stsTokenURL != "" && stsClientID != "" && stsClientSecret != "" {
			if token, err := fetchSTSToken(n.Participant); err != nil {
				logger.Sugar().Errorw("failed to fetch STS token for outbound delivery, falling back to partyDAT", "providerPid", n.ProviderPid, "audience", n.Participant, "attempt", attempt, "error", err)
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

// ErrProviderRequestFailed marks any failure of an outbound call to the
// remote Provider - the initiating Contract Request Message (#80), or (#82)
// the autonomous ACCEPTED event / Agreement Verification Message: a network
// error, a non-2xx response, or (Contract Request Message only) a 2xx
// response missing providerPid. Distinct from ErrNegotiationNotFound /
// ErrInvalidTransition - this is an upstream failure, so the internal API
// maps it to 502, not 404/409.
var ErrProviderRequestFailed = errors.New("outbound negotiation request to provider failed")

// contractRequestAck is the minimal shape needed out of the remote
// Provider's response to an initiating Contract Request Message - the DSP
// ContractNegotiation ack (contract-negotiation-schema.json), just the field
// this side needs to adopt the negotiation (providerPid).
type contractRequestAck struct {
	ProviderPid string `json:"providerPid"`
}

// sendContractRequest POSTs the initiating Contract Request Message to a
// remote Provider's negotiations/request endpoint, synchronously - unlike
// deliverToConsumer's fire-and-forget push, the caller
// (negotiationsConsumerCollectionHandler) needs the real providerPid back
// before it can persist anything durable, so this is a single attempt, no
// retry: on failure the internal-API caller can just retry the whole
// create-as-consumer call, there is nothing partial to clean up first.
func sendContractRequest(providerEndpoint, participant string, n *Negotiation) (string, error) {
	body, err := json.Marshal(map[string]any{
		"@context":        dspContext,
		"@type":           "ContractRequestMessage",
		"consumerPid":     n.ConsumerPid,
		"offer":           n.Offer,
		"callbackAddress": n.CallbackAddress,
	})
	if err != nil {
		return "", fmt.Errorf("%w: marshaling request: %w", ErrProviderRequestFailed, err)
	}

	req, err := http.NewRequest(http.MethodPost, providerEndpoint+"/negotiations/request", bytes.NewReader(body))
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

	var ack contractRequestAck
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		return "", fmt.Errorf("%w: decoding provider response: %w", ErrProviderRequestFailed, err)
	}
	if ack.ProviderPid == "" {
		return "", fmt.Errorf("%w: provider response has no providerPid", ErrProviderRequestFailed)
	}

	return ack.ProviderPid, nil
}

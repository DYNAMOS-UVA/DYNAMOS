package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
)

// jobExecutionClient calls api-gateway's own public data-request entry
// point, the same /api/v1/requestApproval endpoint sqlDataRequest
// ultimately uses (see the T3.2 planning note in
// docs/transfer/dsp-transfer-state-machine.md, and ADR-008).
// transfer-process-service uses it purely as an external HTTP caller,
// the same role loadtest and sql-test already have. This design needs
// no change to api-gateway, orchestrator, policy-enforcer, or agent.
// The non-DSP job pipeline stays untouched.
var jobExecutionClient = http.Client{Timeout: 40 * time.Second}

// triggerJobAndDeliver runs T3.2's job-trigger step for a REQUESTED
// transfer. Call it in its own goroutine. A slow or blocked job then
// never holds up the Transfer Request Message's own HTTP response.
//
// api-gateway's own request handler bounds itself to a 30-second
// context (go/cmd/api-gateway/requests.go). jobExecutionClient's own
// timeout, 40 seconds, stays above that bound. A normal api-gateway
// timeout then surfaces here as a clean HTTP response, not as a
// client-side timeout racing api-gateway's own.
func triggerJobAndDeliver(id string) {
	t, err := store.Get(id)
	if err != nil {
		logger.Sugar().Errorw("job trigger: failed to load transfer", "id", id, "error", err)
		return
	}

	result, err := requestJobExecution(t)
	if err != nil {
		logger.Sugar().Warnw("job trigger: job pipeline call failed, terminating transfer", "id", id, "error", err)
		markTerminatedForJobFailure(id)
		return
	}

	markStartedThenCompleted(id, result)
}

// requestJobExecution builds and sends the api.RequestApproval that
// starts DYNAMOS's existing job-execution pipeline. It reuses two
// fields the DSP Transfer Request Message already carries:
//
//   - Format supplies the request type. The Provider's own catalog sets
//     Format to a "dynamos:<type>" value (see pkg/catalog/build.go's
//     Distribution). This function strips the "dynamos:" prefix to get
//     the request type api-gateway expects, for example
//     "sqlDataRequest".
//   - DataAddress supplies the job body itself: query, algorithm,
//     columns, options. No DSP struct anywhere carries this today.
//     Neither catalog-service's Offer nor negotiation-service's
//     Agreement models a chosen query or archetype. Both stay
//     permission-shaped, not job-shaped. This function reuses
//     DataAddress's already-opaque, already-wired shape for a
//     DYNAMOS-specific purpose. T3.1.4 already set this precedent once.
//     It redefined AgreementId as the owning negotiation's own
//     providerPid. Both choices stay open to revision once a real
//     TCK/interop case demands a different shape.
//
// DataProviders is just this transfer's own party. T3.2's scope note
// keeps the job pipeline itself untouched. A transfer maps to the one
// Provider that already finalized this agreement, not to a fan-out
// across other DYNAMOS parties.
func requestJobExecution(t *TransferProcess) (json.RawMessage, error) {
	if len(t.DataAddress) == 0 {
		return nil, fmt.Errorf("transfer %q carries no job spec (dataAddress) to trigger with", t.ProviderPid)
	}

	approval := api.RequestApproval{
		Type:          strings.TrimPrefix(t.Format, "dynamos:"),
		User:          api.User{UserName: t.Participant},
		DataProviders: []string{t.Party},
		DataRequest:   t.DataAddress,
	}

	body, err := json.Marshal(approval)
	if err != nil {
		return nil, fmt.Errorf("marshaling job request for transfer %q: %w", t.ProviderPid, err)
	}

	req, err := http.NewRequest(http.MethodPost, apiGatewayURL+"/api/v1/requestApproval", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building job request for transfer %q: %w", t.ProviderPid, err)
	}
	req.Header.Set("Content-Type", "application/json")
	// apiGatewayHost sets the name-based routing header the local dev
	// ingress needs (config_local.go). In-cluster calls dial the real
	// DNS name directly (config_prod.go), so this repeats it as a no-op.
	req.Host = apiGatewayHost

	resp, err := jobExecutionClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling job pipeline for transfer %q: %w", t.ProviderPid, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading job pipeline response for transfer %q: %w", t.ProviderPid, err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("job pipeline rejected transfer %q: status %d", t.ProviderPid, resp.StatusCode)
	}
	if !json.Valid(respBody) {
		return nil, fmt.Errorf("job pipeline returned a non-JSON response for transfer %q", t.ProviderPid)
	}

	return json.RawMessage(respBody), nil
}

// markStartedThenCompleted moves a transfer from REQUESTED all the way
// to COMPLETED, once the job pipeline call has already returned a
// result. It moves through STARTED first: transition never allows
// REQUESTED -> COMPLETED directly. It carries the job result on the
// Start message's own dataAddress field. This is the one DSP message
// shape in this state machine already built to carry a payload (see
// transferStartHandler in transfer_handler.go). The DSP Transfer
// Completion Message has no payload field of its own.
//
// This reloads the transfer fresh from the store before each
// transition, instead of reusing the caller's own copy. A Consumer
// could have suspended or terminated this transfer while the job
// pipeline call was still in flight. Either transition call can then
// fail with ErrInvalidTransition. That is not a bug, just a race this
// function lost. It logs and stops rather than overwriting a
// Consumer-driven state.
func markStartedThenCompleted(id string, result json.RawMessage) {
	t, err := store.Get(id)
	if err != nil {
		logger.Sugar().Errorw("job completion: failed to load transfer", "id", id, "error", err)
		return
	}

	if err := t.transition(StateStarted, StateRequested); err != nil {
		logger.Sugar().Warnw("job completion: transfer left REQUESTED before the job pipeline replied, dropping result", "id", id, "state", t.State)
		return
	}
	t.DataAddress = result
	if err := store.Save(t); err != nil {
		logger.Sugar().Errorw("job completion: failed to save STARTED transfer", "id", id, "error", err)
		return
	}
	deliverToConsumer(t, "start", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferStartMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
		"dataAddress": t.DataAddress,
	})

	if err := t.transition(StateCompleted, StateStarted); err != nil {
		logger.Sugar().Warnw("job completion: transfer left STARTED before completion could be sent", "id", id, "state", t.State)
		return
	}
	if err := store.Save(t); err != nil {
		logger.Sugar().Errorw("job completion: failed to save COMPLETED transfer", "id", id, "error", err)
		return
	}
	deliverToConsumer(t, "completion", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferCompletionMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
	})
}

// markTerminatedForJobFailure moves a transfer straight to TERMINATED
// after a failed or timed-out job pipeline call. It skips STARTED: the
// job never produced a result, so nothing started. transition already
// allows TERMINATED from REQUESTED directly (see
// transferTerminationHandler's own from-list in transfer_handler.go).
func markTerminatedForJobFailure(id string) {
	t, err := store.Get(id)
	if err != nil {
		logger.Sugar().Errorw("job failure: failed to load transfer", "id", id, "error", err)
		return
	}

	if err := t.transition(StateTerminated, StateRequested, StateStarted, StateSuspended); err != nil {
		logger.Sugar().Warnw("job failure: transfer already left a non-terminal state, dropping termination", "id", id, "state", t.State)
		return
	}
	if err := store.Save(t); err != nil {
		logger.Sugar().Errorw("job failure: failed to save TERMINATED transfer", "id", id, "error", err)
		return
	}
	deliverToConsumer(t, "termination", map[string]any{
		"@context":    dspContext,
		"@type":       "TransferTerminationMessage",
		"providerPid": t.ProviderPid,
		"consumerPid": t.ConsumerPid,
	})
}

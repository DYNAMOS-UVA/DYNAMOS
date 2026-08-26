# DSP Transfer Process: State Machine, Messages, Endpoints

Tracks [issue #61](https://github.com/DYNAMOS-UVA/DYNAMOS/issues/61), T3.1.1 of [Phase 3 - Transfer Process Service](https://github.com/DYNAMOS-UVA/DYNAMOS/milestones). Doc only, no code - same shape as T2.1's `docs/negotiation/dsp-negotiation-state-machine.md` (#42) and T1.1's `docs/catalog/dynamos-catalog-schema.md` (#4).

## Implementation reference

- DSP Transfer Process Protocol: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#transfer-protocol
- DSP Transfer Process HTTPS Binding: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#transfer-http

Built directly against the normative JSON Schemas, prose spec, and examples in the spec's source repo (`eclipse-dataspace-protocol-base/DataspaceProtocol`, commit `0b7ac408076e61dbe96bfd72a7676fb847567581` - same commit the catalog and negotiation vendoring pinned), vendored locally in [`spec-reference/`](./spec-reference/):

- `spec-reference/specifications/transfer.process.protocol.md` - state list, state machine diagram, message-type semantics
- `spec-reference/specifications/transfer.process.binding.https.md` - provider + consumer HTTPS path bindings
- `spec-reference/transfer/*-schema.json` - structural requirements per message
- `spec-reference/transfer/example/*.json` - reference examples per message
- `spec-reference/specifications/figures/transfer-process-state-machine.{png,puml}` - the state diagram itself

See [`spec-reference/README.md`](./spec-reference/README.md) for the full file layout and provenance.

## Control plane vs data plane

The spec draws a hard line: a Transfer Process has a **control plane** (messages that move the state machine - what this document and `transfer-process-service` cover) and a **data plane** (the actual bytes moving between Provider and Consumer, over a wire protocol the spec explicitly puts out of scope). Data transfers are `push` (Provider's data plane sends to a Consumer endpoint) or `pull` (Consumer's data plane retrieves from a Provider endpoint), and data is `finite` (ends, then the process completes) or `non-finite` (streams, continues until explicit termination).

DYNAMOS's existing job-execution pipeline (gRPC/RabbitMQ, wired in T3.2) is the data plane here. This document and T3.1.1-T3.1.4 only cover the control-plane lifecycle signals layered on top of it.

## States (5)

| State | Meaning |
|---|---|
| `REQUESTED` | Consumer requested a Dataset under an Agreement; Provider ACKed. |
| `STARTED` | The Dataset is available to the Consumer, or the Provider has begun pushing data. |
| `COMPLETED` | The transfer finished (either party can report this). |
| `SUSPENDED` | The transfer is paused (either party can suspend). |
| `TERMINATED` | The Transfer Process ended (either party can terminate). Terminal - no further transitions. |

Transitions are labeled `C` (Consumer-sent message) or `P` (Provider-sent message) in the spec diagram (`spec-reference/specifications/figures/transfer-process-state-machine.png`). Terminal states (`COMPLETED`, `TERMINATED`) are final; the state machine MUST NOT transition out of them. Compared to negotiation's 7-state machine, this one is simpler: no intermediate accept/agree/verify round, and `SUSPENDED` gives it a pause/resume path negotiation doesn't have.

## Messages (5)

| Message | Sent by | Resulting state | Carries |
|---|---|---|---|
| Transfer Request Message | Consumer | `REQUESTED` | `consumerPid`, `agreementId` (must reference an existing Agreement), `format` (Distribution format from the Provider's Catalog), `dataAddress` (only if `format` requires a push transfer), `callbackAddress` (URI for messages back to the Consumer - Provider MUST error if unrecognized) |
| Transfer Start Message | Provider (initial start); also Consumer (resume after `SUSPENDED`) | `STARTED` | `providerPid`, `consumerPid`, `dataAddress` (required for a pull transfer - transport-specific endpoint the Consumer fetches from) |
| Transfer Suspension Message | Consumer or Provider | `SUSPENDED` | `providerPid`, `consumerPid`, optional `code`/`reason[]` |
| Transfer Completion Message | Consumer or Provider | `COMPLETED` | `providerPid`, `consumerPid` |
| Transfer Termination Message | Consumer or Provider | `TERMINATED` | `providerPid`, `consumerPid`, optional `code`/`reason[]`. Valid from any non-terminal state. |

Every message gets an `ACK` (the `TransferProcess` resource: `providerPid`, `consumerPid`, `state`) or an `ERROR` (`TransferError`: `code`, `reason[]`) in response. Neither is its own state transition - the transition already happened when the triggering message was accepted.

Providers SHOULD treat Transfer Request Messages as idempotent per `consumerPid`: a repeat of an already-processed request should get back a Transfer Start Message, not a second `REQUESTED` transition.

## Provider-initiated-Start vs Consumer-resume-Start asymmetry

The Transfer Start Message has two distinct senders depending on why it is sent, and the spec gives each sender its own transport route:

- **Initial start** (`REQUESTED` -> `STARTED`, the normal happy path): sent by the **Provider**, delivered **outbound** to the Consumer's callback endpoint (`POST {callbackAddress}/transfers/:consumerPid/start`). There is no inbound provider-path endpoint for this case.
- **Resume after suspend** (`SUSPENDED` -> `STARTED`): sent by the **Consumer**, delivered **inbound** to the Provider's `POST /transfers/:providerPid/start` endpoint.

Practical effect for T3.1.4: `dsp-connector`'s inbound `/transfers/:providerPid/start` handler only ever handles the resume-after-suspend case. The initial `STARTED` transition is something `transfer-process-service` sends out, not something `dsp-connector` receives. This is the same asymmetric shape negotiation's Offer/Agreement messages had (provider-initiated messages go out via callback, never come in on a provider-path route) - see `docs/negotiation/dsp-negotiation-state-machine.md`'s Endpoints section.

## Endpoints

### Provider endpoints (6) - DYNAMOS implements these

DYNAMOS is always the data Provider (matches catalog's and negotiation's existing scope). These 6 are what `dsp-connector` terminates (T3.1.4).

| Method | Path | Triggering message | Resulting state |
|---|---|---|---|
| `GET` | `/transfers/:providerPid` | - (read) | returns current state |
| `POST` | `/transfers/request` | Transfer Request Message | `REQUESTED` |
| `POST` | `/transfers/:providerPid/start` | Transfer Start Message (**Consumer resume-after-suspend only**, see asymmetry above) | `STARTED` |
| `POST` | `/transfers/:providerPid/completion` | Transfer Completion Message | `COMPLETED` |
| `POST` | `/transfers/:providerPid/termination` | Transfer Termination Message | `TERMINATED` |
| `POST` | `/transfers/:providerPid/suspension` | Transfer Suspension Message | `SUSPENDED` |

### Consumer callback endpoints (4) - out of scope for DYNAMOS as an inbound surface, but must be called outbound

Callback-based: relative to the `callbackAddress` the Consumer supplied in its Transfer Request Message. DYNAMOS never plays Consumer today, so these are never *implemented* as inbound routes. But unlike negotiation's callback set, `transfer-process-service` (as Provider) must *call* the start/completion/termination/suspension callbacks itself - this is the outbound delivery T3.1.3 builds.

| Method | Path | Triggering message | Resulting state |
|---|---|---|---|
| `POST` | `/:callback/transfers/:consumerPid/start` | Transfer Start Message (Provider, **initial start**) | `STARTED` |
| `POST` | `/:callback/transfers/:consumerPid/completion` | Transfer Completion Message | `COMPLETED` |
| `POST` | `/:callback/transfers/:consumerPid/termination` | Transfer Termination Message | `TERMINATED` |
| `POST` | `/:callback/transfers/:consumerPid/suspension` | Transfer Suspension Message | `SUSPENDED` |

## DYNAMOS concept mapping

| DSP concept | DYNAMOS concept | Notes |
|---|---|---|
| Transfer Process (state + `providerPid`/`consumerPid`) | New `/dsp/transfers/{id}` etcd key, owned by a new **`transfer-process-service`** | Own key namespace, no shared schema with non-DSP keys. Mirrors `negotiation-service`'s own etcd ownership pattern. |
| Provider HTTP endpoints (6) | New handlers in **`dsp-connector`** (`transfer_handler.go`/`transfer_client.go`, mirroring `negotiation_handler.go`/`negotiation_client.go`) | `dsp-connector` terminates the protocol, does auth/error-mapping, and an ownership check on every endpoint but the initiating request (same IDOR-fix pattern as T2.3). Calls `transfer-process-service` internally for state, same shape as its existing `negotiation-service`/`catalog-service` calls. |
| `agreementId` validation on Transfer Request | `agreementId` MUST resolve to a `FINALIZED` negotiation | Exact owning service (`transfer-process-service` calling `negotiation-service`'s internal API, vs. the check landing in `dsp-connector`) is still open - T3.1.3/T3.1.4's call, same shape as Phase 2's own `offer.@id` validation question, resolved during implementation. |
| Data availability at `STARTED` | Trigger into DYNAMOS's existing job-execution pipeline (`api-gateway`, same entry point `sqlDataRequest` uses) | Deferred to T3.2 - out of scope for this document and for T3.1's code (T3.1.1-T3.1.4). Already flagged in the Phase 3 planning doc: T3.2 touches `api-gateway`'s job-trigger path itself, a real risk of a genuine conflict needing escalation, not an isolated new key. |
| Consumer callback delivery (`start`/`completion`/`termination`/`suspension`) | Outbound HTTP calls from `transfer-process-service` to the Consumer's `callbackAddress` | Built in T3.1.3, from the start, not deferred - unlike negotiation-service, which shipped with zero outbound delivery until T2.5's TCK pass forced it in as a bug fix. Transfer process is more outbound-heavy: Provider-initiated Start is the normal happy path here, not an edge case. |
| Consumer role / consumer endpoints as an inbound surface | Not implemented | DYNAMOS is provider-only today, matching catalog's and negotiation's existing scope. Deferred to a future issue if a real interop need appears (e.g. a Phase 4 demo against a reference EDC connector). |
| Push/pull, finite/non-finite data plane distinctions | Not modeled by DYNAMOS's DSP layer | Out of the DSP Transfer Process Protocol's own scope (control plane only) and out of this document's scope - DYNAMOS's data plane is the pre-existing job-execution pipeline, untouched by this control-plane wrapper. |

## Still open (deferred to T3.1.2+)

- Exact etcd key shape for `/dsp/transfers/{id}` (fields beyond `providerPid`/`consumerPid`/`state` - e.g. whether `agreementId`, `format`, `dataAddress`, `callbackAddress` get persisted alongside state) - T3.1.2/T3.1.3's design task.
- Which service owns the `agreementId` -> `FINALIZED` negotiation check (see mapping table above) - T3.1.3/T3.1.4's task.
- How a `TERMINATED` or `COMPLETED` Transfer Process's etcd key gets cleaned up, if at all (spec allows but doesn't require removal after a terminal state) - same open question negotiation left for its own terminal states.
- Identity/auth for the 6 provider endpoints - likely inherits `dsp-connector`'s existing placeholder (`Authorization` header as participant email, no token verification), matching catalog's and negotiation's current placeholder, but not decided here.
- Whether `transfer-process-service`'s internal API mirrors `negotiation-service`'s verb-per-DSP-message shape exactly (start/complete/suspend/terminate) or needs its own additional verbs for the outbound-delivery bookkeeping - T3.1.3's task.

## Related

- [[phase3-transfer-process-issues]] - the drafted T3.1-T3.4 issue set this doc's mapping section restates
- `docs/negotiation/dsp-negotiation-state-machine.md` - the T2.1 precedent this doc follows the shape of, and the direct architectural precedent for the outbound-delivery and provider/consumer-asymmetry patterns
- [[negotiation-service]], [[dsp-connector]], [[catalog-service]] - existing modules this phase extends

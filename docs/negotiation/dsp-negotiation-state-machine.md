# DSP Contract Negotiation: State Machine, Messages, Endpoints

Tracks [issue #42](https://github.com/DYNAMOS-UVA/DYNAMOS/issues/42), T2.1 of [Phase 2 - Contract Negotiation Service](https://github.com/DYNAMOS-UVA/DYNAMOS/milestones). Doc only, no code - same shape as T1.1's `docs/catalog/dynamos-catalog-schema.md` (#4).

## Implementation reference

- DSP Contract Negotiation Protocol: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#contract-negotiation-protocol
- DSP Contract Negotiation HTTPS Binding: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#negotiation-http

Built directly against the normative JSON Schemas, prose spec, and examples in the spec's source repo (`eclipse-dataspace-protocol-base/DataspaceProtocol`, commit `0b7ac408076e61dbe96bfd72a7676fb847567581` - same commit the catalog vendoring pinned), vendored locally in [`spec-reference/`](./spec-reference/):

- `spec-reference/specifications/contract.negotiation.protocol.md` - state list, state machine diagram, message-type semantics
- `spec-reference/specifications/contract.negotiation.binding.https.md` - provider + consumer HTTPS path bindings
- `spec-reference/negotiation/*-schema.json` - structural requirements per message
- `spec-reference/negotiation/example/*.json` - reference examples per message
- `spec-reference/specifications/figures/contract.negotiation.state.machine.{png,puml}` - the state diagram itself

See [`spec-reference/README.md`](./spec-reference/README.md) for the full file layout and provenance.

## States (7)

A Contract Negotiation is uniquely identified by an IRI and tracked independently, but identically, by both parties. If the two sides' states ever diverge, the negotiation MUST be terminated - there is no reconciliation path, only starting a new one.

| State | Meaning |
|---|---|
| `REQUESTED` | Consumer requested an Agreement for a Dataset based on an Offer; Provider ACKed. |
| `OFFERED` | Provider sent an Offer; Consumer ACKed. |
| `ACCEPTED` | Consumer accepted the latest Offer; Provider ACKed. |
| `AGREED` | Provider accepted the latest Offer, sent an Agreement; Consumer ACKed. |
| `VERIFIED` | Consumer sent Agreement verification; Provider ACKed. |
| `FINALIZED` | Provider sent finalization (including its own Agreement verification); Consumer ACKed. **Data is now available to the Consumer.** |
| `TERMINATED` | Either party sent a termination message; the other ACKed. Terminal - no further transitions. A brand-new negotiation (new IRI) may be started instead. |

Transitions are labeled `C` (Consumer-sent message) or `P` (Provider-sent message) in the spec diagram (`spec-reference/specifications/figures/contract.negotiation.state.machine.png`). Every state transition happens only on a message being sent **and acknowledged** - an unacknowledged message doesn't move the state machine.

Both `REQUESTED` and `OFFERED` can be reached repeatedly before `AGREED` - either party can counter-offer via a further Contract Request or Contract Offer message, so the negotiation can loop between `REQUESTED`/`OFFERED` before either side accepts.

## Messages (6)

| Message | Sent by | Resulting state(s) | Carries |
|---|---|---|---|
| Contract Request Message | Consumer | `REQUESTED`, `TERMINATED` | `offer` (must include `offer.@id`; `offer.@id` must resolve to an Offer in a real Catalog - Provider errors if not), optional `providerPid` (counter-request vs. new negotiation), `callbackAddress` (async delivery target) |
| Contract Offer Message | Provider | `OFFERED`, `TERMINATED` | `offer` (with `target`), optional `consumerPid`, `callbackAddress` on the initiating message |
| Contract Agreement Message | Provider | `AGREED`, `TERMINATED` | full `agreement` object: `@id`, `target` (Dataset id), `timestamp`, `assigner`/`assignee` (dataspace-scoped party identifiers), `permission`/`prohibition`/`obligation` |
| Contract Agreement Verification Message | Consumer | `VERIFIED`, `TERMINATED` | just `consumerPid`/`providerPid` - Provider errors if the Agreement can't be validated |
| Contract Negotiation Event Message | Consumer or Provider | `ACCEPTED` (Consumer, `eventType: ACCEPTED`) / `FINALIZED` (Provider, `eventType: FINALIZED`) / `TERMINATED` | `eventType`. Cross-sending is an error: Consumer MUST NOT send `FINALIZED`, Provider MUST NOT send `ACCEPTED`. No events after a terminal state. |
| Contract Negotiation Termination Message | Consumer or Provider | `TERMINATED` | `consumerPid`/`providerPid`, optional human-readable reason. Valid from any state, no explanation required. |

Every message gets an `ACK` (the `ContractNegotiation` resource itself: `providerPid`, `consumerPid`, `state`) or an `ERROR` (`ContractNegotiationError`: `code`, `reason[]`) in response. Neither is its own state transition - the transition already happened when the triggering message was accepted.

## Endpoints

### Provider endpoints (6) - DYNAMOS implements these

DYNAMOS is always the data Provider (matches the catalog's existing scope). These 6 are what `dsp-connector` terminates (T2.3).

| Method | Path | Triggering message | Resulting state |
|---|---|---|---|
| `GET` | `/negotiations/:providerPid` | - (read) | returns current state |
| `POST` | `/negotiations/request` | Contract Request Message (initiating, no `providerPid` yet) | `REQUESTED` |
| `POST` | `/negotiations/:providerPid/request` | Contract Request Message (counter-request) | `REQUESTED` (re-entrant) |
| `POST` | `/negotiations/:providerPid/events` | Contract Negotiation Event Message (`eventType: ACCEPTED`) | `ACCEPTED` |
| `POST` | `/negotiations/:providerPid/agreement/verification` | Contract Agreement Verification Message | `VERIFIED` |
| `POST` | `/negotiations/:providerPid/termination` | Contract Negotiation Termination Message | `TERMINATED` |

Note the asymmetry: the Provider *sends* the Contract Offer Message and the Contract Agreement Message (the `OFFERED` and `AGREED` transitions), it doesn't receive them on a provider-path endpoint - those go out to the Consumer's callback endpoints instead (see below). The Provider also *sends* the `FINALIZED` event to the Consumer's callback, it doesn't receive it on a provider path.

### Consumer endpoints (6) - out of scope for DYNAMOS, deferred

Callback-based: relative to the `callbackAddress` the Consumer supplied in its initiating Contract Request Message. DYNAMOS never plays Consumer today, so these aren't implemented. Listed for completeness / future Phase 4 interop demo:

| Method | Path | Triggering message | Resulting state |
|---|---|---|---|
| `GET` | `/:callback/negotiations/:consumerPid` | - (read) | returns current state |
| `POST` | `/negotiations/offers` | Contract Offer Message (initiating, no `consumerPid` yet) | `OFFERED` |
| `POST` | `/:callback/negotiations/:consumerPid/offers` | Contract Offer Message (counter-offer) | `OFFERED` (re-entrant) |
| `POST` | `/:callback/negotiations/:consumerPid/agreement` | Contract Agreement Message | `AGREED` |
| `POST` | `/:callback/negotiations/:consumerPid/events` | Contract Negotiation Event Message (`eventType: FINALIZED`) | `FINALIZED` |
| `POST` | `/:callback/negotiations/:consumerPid/termination` | Contract Negotiation Termination Message | `TERMINATED` |

## DYNAMOS concept mapping

Decided in the 2026-07-16 Phase 2 drafting session (wiki `meta/phase2-contract-negotiation-issues.md`), restated here as the T2.1 deliverable:

| DSP concept | DYNAMOS concept | Notes |
|---|---|---|
| Contract Negotiation (state + `providerPid`/`consumerPid`) | New `/dsp/negotiations/{id}` etcd key, owned by a new **`negotiation-service`** | Own key namespace, no shared schema with non-DSP keys. Mirrors `catalog-service`'s own etcd ownership pattern. |
| Provider HTTP endpoints (6) | New handlers in **`dsp-connector`** (`negotiation_handler.go`/`negotiation_client.go`, mirroring `catalog_handler.go`/`catalog_client.go`) | `dsp-connector` terminates the protocol and does auth/error-mapping, same role it already plays for catalog. Calls `negotiation-service` internally for state, same shape as its existing `catalog-service` call. |
| `offer.@id` validation on Contract Request | `dsp-connector` calls its existing `catalog-service` client | Decided explicitly: `negotiation-service` stays free of any `catalog-service` dependency, purely owns state. Validation happens one hop earlier, in `dsp-connector`. |
| Agreement (`assigner`/`assignee`/`permission`/`target`) | DYNAMOS `Relation` (per-counterparty grant) | Same relationship the catalog's `Offer` already models - `assigner`/`assignee` line up with the existing per-`Relation` party pair used to scope catalog responses. |
| `FINALIZED` transition (data now available) | Write into `/policyEnforcer/agreements/{party}` (existing etcd key `policy-enforcer` already reads) | The concrete mechanism DYNAMOS uses to grant real access - see below. No `policy-enforcer` code change; option B from the drafting session. |
| ODRL `permission`/`constraint` in the Agreement | `Relation.RequestTypes` / `Relation.AllowedArchetypes` / `Relation.AllowedComputeProviders` | Same `dynamos:archetype`/`dynamos:computeProvider` constraint profile the catalog's worked example (`docs/catalog/dynamos-catalog-schema.md` decision 5) already defined - the negotiation's Agreement is built from the same vocabulary, not a new one. |
| Consumer role / consumer endpoints | Not implemented | DYNAMOS is provider-only today, matching the catalog's existing scope. Deferred to a future issue if a real interop need appears (e.g. Phase 4 demo). |

### The `FINALIZED` write, concretely (feeds T2.4)

On receiving/sending the `FINALIZED` event, `negotiation-service` must:

1. Read the current value at `/policyEnforcer/agreements/{party}` (etcd).
2. Unmarshal into `api.Agreement` (`Relations map[string]api.Relation`) - the exact struct `policy-enforcer`'s `generate_validation_response.go` already reads.
3. Add/update `Relations[consumerEmail]`: `ID` (the negotiation id), `RequestTypes` (from the negotiated Offer's `permission` actions), `DataSets: [target]` (the Agreement's `target`, i.e. the agreed Dataset id), `AllowedArchetypes`/`AllowedComputeProviders` (from the Offer's `dynamos:archetype`/`dynamos:computeProvider` constraints).
4. Write the full document back - **must** be read-modify-write. A blind `put` on that key drops every other party's Relation already stored there (same clobber hazard the `adding-a-dataset` runbook already documents for manual dataset seeding).

This is the concrete mechanism behind "data is now available to the Consumer" in the `FINALIZED` state's spec definition: DYNAMOS's version of "available" is a working `policy-enforcer` agreement entry, verified by a normal (non-DSP) `sqlDataRequest` succeeding right after, no `policy-enforcer` restart needed.

## Still open (deferred to T2.2+)

- Exact etcd key shape for `/dsp/negotiations/{id}` (fields beyond `providerPid`/`consumerPid`/`state` - e.g. whether the full Offer/Agreement bodies get persisted alongside the state, or only derived at write time for T2.4) - T2.2's design task.
- How a `TERMINATED` negotiation's `/dsp/negotiations/{id}` key gets cleaned up, if at all (spec allows but doesn't require removal after a terminal state).
- Identity/auth for the 6 provider endpoints - likely inherits `dsp-connector`'s existing placeholder (`Authorization` header as participant email, no token verification), matching the catalog endpoints' current Phase 1 placeholder, but not decided here.
- Whether `negotiation-service`'s internal API mirrors `catalog-service`'s `/internal/v1/catalog` shape exactly or needs negotiation-specific verbs (start/counter/accept/verify/finalize/terminate) - T2.2.2's task.

## Related

- [[phase2-contract-negotiation-issues]] - the drafted T2.1-T2.6 issue set this doc's mapping section restates
- [[ADR-008 DSP Layer Strictly Additive]] - standing constraint this and all Phase 2 work is built under
- `docs/catalog/dynamos-catalog-schema.md` - the T1.1 precedent this doc follows the shape of
- [[dsp-connector]], [[catalog-service]] - existing modules this phase extends

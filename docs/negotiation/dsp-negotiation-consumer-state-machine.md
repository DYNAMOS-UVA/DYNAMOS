# DSP Contract Negotiation, Consumer Role: State Machine, Messages, Endpoints

Tracks [issue #79](https://github.com/DYNAMOS-UVA/DYNAMOS/issues/79). Doc only, no code - same shape as T2.1's [`dsp-negotiation-state-machine.md`](./dsp-negotiation-state-machine.md), flipped to DYNAMOS playing Consumer instead of Provider.

## Why this doc exists

T2.1 covered DYNAMOS as Provider only - the only role DYNAMOS played at the time. T2.1 §"DYNAMOS concept mapping" explicitly deferred the Consumer role: "Not implemented... Deferred to a future issue if a real interop need appears."

That need appeared. Per the 2026-08-03 standup (`wiki/standups/2026-08-03.md`, decisions 1-2), Thomas, Alexandros, and Ana confirmed Consumer-role priority is high, build now - framed as building DSP infrastructure/interfaces first, complex workflows researched later. This doc is that scoping step, mirroring T2.1's shape for the Consumer role.

## Implementation reference

Same vendored spec as T2.1, no new vendoring needed - the HTTPS binding doc already carries both path bindings in one file:

- DSP Contract Negotiation Protocol: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#contract-negotiation-protocol
- DSP Contract Negotiation HTTPS Binding: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#negotiation-http
- `spec-reference/specifications/contract.negotiation.binding.https.md` §"Consumer Path Bindings" (line 193 onward) - the 6 callback paths this doc scopes into DYNAMOS work
- `spec-reference/negotiation/*-schema.json` / `spec-reference/negotiation/example/*.json` - same schemas as T2.1, message shape doesn't change by role

## States (7) - unchanged from T2.1

The state machine is the same 7 states, same transitions, same terminal `TERMINATED` - a Contract Negotiation has exactly one state machine, tracked identically (not separately) by both parties. See T2.1's [state table](./dsp-negotiation-state-machine.md#states-7). This doc only concerns which side sends/receives which message.

## Endpoints

### Consumer endpoints (4 inbound + 1 outbound) - this doc's scope

Callback-based: relative to the `callbackAddress` DYNAMOS itself supplies when it sends the initiating Contract Request Message. The Provider POSTs every later message to `{callbackAddress}/negotiations/{consumerPid}/{path}`.

| Method | Path | Triggering message | Resulting state | Direction |
|---|---|---|---|---|
| `POST` | `/negotiations/request` (own outbound call, not a callback) | Contract Request Message (initiating) | `REQUESTED` | DYNAMOS sends |
| `POST` | `:callback/negotiations/:consumerPid/offers` | Contract Offer Message | `OFFERED` | DYNAMOS receives |
| `POST` | `:callback/negotiations/:consumerPid/agreement` | Contract Agreement Message | `AGREED` | DYNAMOS receives |
| `POST` | `:callback/negotiations/:consumerPid/events` (`eventType: FINALIZED`) | Contract Negotiation Event Message | `FINALIZED` | DYNAMOS receives |
| `POST` | `:callback/negotiations/:consumerPid/termination` | Contract Negotiation Termination Message | `TERMINATED` | DYNAMOS receives (or sends) |

`GET :callback/negotiations/:consumerPid` (read-only state lookup, Provider-initiated) is out of scope here - low value with no known caller, add later if a real need shows up.

Two messages DYNAMOS must *send*, not just receive, once it decides to accept:

| Message | Sent by DYNAMOS on | Resulting state |
|---|---|---|
| Contract Negotiation Event Message, `eventType: ACCEPTED` | after receiving an acceptable Offer | `ACCEPTED` |
| Contract Agreement Verification Message | after receiving the Agreement | `VERIFIED` |

Both of these are DYNAMOS calling out to the Provider's own provider-path endpoints (`/negotiations/:providerPid/events`, `/negotiations/:providerPid/agreement/verification`) - the same 6 endpoints T2.1 already documents as `dsp-connector`'s existing provider-role implementation, just called as a client this time instead of served.

### Provider endpoints - unchanged, already implemented (T2.3)

DYNAMOS keeps playing Provider too - the two roles aren't exclusive, a single `dsp-connector` instance plays both depending on which negotiation it's in. No change to T2.1/T2.3's provider-role work.

## DYNAMOS concept mapping

| DSP concept | DYNAMOS concept | Notes |
|---|---|---|
| Consumer HTTP callback endpoints (4) | New handlers in `dsp-connector` (likely `negotiation_consumer_handler.go`, mirroring the existing `negotiation_handler.go`) | Same termination/auth/error-mapping role `dsp-connector` already plays for the provider path. |
| Outbound Contract Request Message (negotiation-initiate) | New outbound call in `dsp-connector`, triggered by a new endpoint scoped only for TCK/dev use for now (real production trigger still open, see below) | Builds and sends the initiating message to some other party's provider endpoint. |
| "We are Consumer in this negotiation" vs. T2.1's always-Provider assumption | New `Kind` field on `negotiation.Negotiation` (`Provider` / `Consumer`) | Today's `Negotiation` struct (`go/cmd/negotiation-service/negotiation.go`) has no such field - it implicitly assumes DYNAMOS is always Provider (`ProviderPid` always locally generated, `ConsumerPid` always the other side's). A Consumer-role negotiation flips which Pid is locally-owned. Needs its own etcd key split or a `Kind`-scoped prefix so a Provider-role and a Consumer-role negotiation never collide on lookup. |
| Autonomous Offer/Agreement decision | New decision point in `dsp-connector` or `negotiation-service`, not built yet anywhere | Per standup decision 1: agents don't participate in policy enforcement today, DSP needs them to respond autonomously. First version: trivial accept-all, no real evaluation. Scoped as its own follow-up issue, not this doc. |
| Consortium agreement ID (Thomas's proposal) | Future `dynamos:consortiumAgreementId` ODRL constraint, same vocabulary slot as existing `dynamos:archetype`/`dynamos:computeProvider` | Confirmed next step after trivial accept-all (standup decision 3), not blocking, not in this doc's scope. |
| Negotiation-initiate trigger (what starts a Consumer-role negotiation) | Still open - standup question 4 | Two candidates: a simple external/admin-triggered endpoint (cheap, matches "infrastructure first"), or `orchestrator` triggering it mid-job when it finds a needed provider relation missing (bigger scope, touches job composition). Needs Alex/Ana's call. This doc's endpoint table above scopes only the cheap version, for TCK/dev triggering - not a claim on the real answer. |

## Still open (deferred to follow-up issues)

- Exact `Kind`/etcd-key-split design for `negotiation.Negotiation` - implementation issue's task, not this doc's.
- Autonomous accept/reject decision logic - trivial accept-all first, its own issue.
- Real (non-TCK) negotiation-initiate trigger mechanism - blocked on standup question 4, Alex/Ana's call.
- Consortium agreement ID constraint - sequenced after accept-all, own future issue.
- Identity/auth for the 4 consumer endpoints - inherits the same placeholder T2.1 already notes for the provider endpoints (`Authorization` header as participant identity, no token verification yet).

## Related

- [[DSP Contract Negotiation State Machine]] (`dsp-negotiation-state-machine.md`) - the T2.1 provider-role doc this one mirrors
- [[phase2-contract-negotiation-issues]] - original provider-role issue set, source of the "deferred" note this doc resolves
- [[2026-08-03-standup-questions]] - identity model, consumer priority, consortium agreement ID, and negotiation-trigger decisions this doc's mapping section relies on
- [[dsp-connector]], [[negotiation-service]] - existing modules this phase extends

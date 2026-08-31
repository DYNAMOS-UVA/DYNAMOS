# DYNAMOS DSP Layer, Testing and Operations Guide

This document explains the Dataspace Protocol (DSP) layer in DYNAMOS. It follows the ASD-STE100 Simplified Technical English writing rules: short sentences, active voice, and one idea per sentence. It names each service, script, and file directly. It does not use vague words such as "the demo" or "the script" without a name attached.

This document has five topics:

- the four DSP microservices and the decisions behind them
- the DSP Technology Compatibility Kit (TCK) and its CI workflow
- how DYNAMOS works with Eclipse EDC's Minimum Viable Dataspace (MVD)
- the demo scripts and their run order
- the findings from a recent code review

This document is a supplement. It does not replace the top-level `README.md` file.

## Table of Contents

1. [Overview](#1-overview)
2. [DSP Microservices](#2-dsp-microservices)
   - [2.1 Architecture at a Glance](#21-architecture-at-a-glance)
   - [2.2 catalog-service](#22-catalog-service)
   - [2.3 dsp-connector](#23-dsp-connector)
   - [2.4 negotiation-service](#24-negotiation-service)
   - [2.5 transfer-process-service](#25-transfer-process-service)
   - [2.6 Patterns Shared Across the Four Services](#26-patterns-shared-across-the-four-services)
3. [Design Decisions](#3-design-decisions)
4. [TCK Testing and CI](#4-tck-testing-and-ci)
5. [MVD Interaction](#5-mvd-interaction)
6. [Demo Setup and Scripts](#6-demo-setup-and-scripts)
7. [Code Review Findings](#7-code-review-findings)

---

## 1. Overview

DYNAMOS implements the Dataspace Protocol (DSP) as an added layer. This layer sits on top of the existing, non-DSP DYNAMOS system. The existing system has five services: `api-gateway`, `orchestrator`, `policy-enforcer`, `agent`, and `sidecar`. It also has a RabbitMQ/gRPC microservice-chain mechanism.

The DSP layer has four services:

- `catalog-service`
- `dsp-connector`
- `negotiation-service`
- `transfer-process-service`

None of these four services import code from the five existing services. None of these four services change the five existing services. Each of the four services is a standalone Go program under `go/cmd/`. Each service has its own `main.go` file. Each service has its own `config_local.go` and `config_prod.go` files, selected by the `local` build tag. Each service has its own etcd key range. Three of the four services (`catalog-service`, `negotiation-service`, `transfer-process-service`) also have their own internal HTTP API.

The DSP layer follows one strict rule: it must not change the behavior of the existing system. An operator who does not use the DSP layer must see the existing system work exactly as before. [Section 3](#3-design-decisions) explains this rule in more detail. Section 3 also describes two real conflicts with this rule and how the DYNAMOS team resolved them.

DYNAMOS deploys one copy of each DSP service for each data-steward party. The `DATA_STEWARD_NAME` environment variable sets the party name for a given copy. The example setup in this document uses three parties: `UVA`, `VU`, and `SURF`. This matches the deployment pattern already used by `agent` and `sidecar`.

---

## 2. DSP Microservices

### 2.1 Architecture at a Glance

```
external DSP participant (or the TCK, or another DYNAMOS instance)
        |  HTTPS, DSP wire protocol, Bearer DAT
        v
   dsp-connector          <-- the only DSP-facing HTTP surface: checks the
                              DAT, checks ownership, and converts internal
                              errors to DSP error responses
        |  internal HTTP (cluster-internal, no authentication)
        +--> catalog-service            (reads catalog and dataset data from etcd)
        +--> negotiation-service        (runs the contract negotiation state machine)
        +--> transfer-process-service   (runs the transfer process state machine;
                                          on a job trigger, calls api-gateway's
                                          existing /api/v1/requestApproval endpoint,
                                          which starts DYNAMOS's existing,
                                          non-DSP job pipeline)
```

`dsp-connector` does not take part in the RabbitMQ/gRPC microservice chain. `dsp-connector` has no sidecar. `dsp-connector` does not use gRPC. `dsp-connector` does not use RabbitMQ. `dsp-connector` is a plain HTTP server.

`catalog-service`, `negotiation-service`, and `transfer-process-service` follow the same pattern. Each of these three services is a plain HTTP server. Each of these three services connects to etcd directly. None of these three services use a sidecar or gRPC.

In local development, each service listens on its own port:

- `catalog-service` listens on port 8091.
- `dsp-connector` listens on port 8090.
- `negotiation-service` listens on port 8092.
- `transfer-process-service` listens on port 8093.

All four services connect to etcd at `localhost:2379` in local development.

In production (the `!local` build tag), all four services listen on port 8080. In production, the services find each other through cluster DNS names. In production, the services connect to a 3-node etcd cluster.

### 2.2 catalog-service

`catalog-service` reads catalog and dataset data from etcd for one party. `catalog-service` has no knowledge of the DSP protocol. `catalog-service` replaced `dsp-connector`'s earlier catalog source, which read from a static JSON file.

`catalog-service` has three endpoints. All three endpoints are internal only. None of the three endpoints require authentication.

- `GET /health` — reports service health.
- `GET /internal/v1/catalog?participant={id}` — returns the full catalog document for one participant.
- `GET /internal/v1/catalog/datasets/{id}?participant={id}` — returns one dataset document.

`catalog-service` builds a catalog response in five steps:

1. `catalog-service` reads the etcd key `/policyEnforcer/agreements/{party}`. `policy-enforcer` also reads this same key for the non-DSP request path.
2. `catalog-service` looks up the requesting participant inside that agreement's relation list. If `catalog-service` finds no match, `catalog-service` returns the error `participant-not-found` with HTTP status 404.
3. For each dataset name in the matched relation, `catalog-service` reads the etcd key `/datasets/{name}`.
4. `catalog-service` builds the catalog and dataset documents with the shared `pkg/catalog` package. `dsp-connector`'s earlier static-file catalog source used the same package.
5. `catalog-service` builds the party's data-access agent URL from a fixed pattern: `http://{party-lower}.{party-lower}.svc.cluster.local:8080/agent/v1/sqlDataRequest/{party-lower}`. DYNAMOS stores no separate agent-endpoint field for this value today.

`catalog-service` reads etcd on every request. `catalog-service` keeps no cache. `catalog-service` does not use etcd's `Watch` feature. The DYNAMOS team accepted this design because catalog reads happen rarely, not on a hot path.

### 2.3 dsp-connector

`dsp-connector` is the only DSP-facing HTTP surface in DYNAMOS. `dsp-connector` receives every DSP wire message: Catalog messages, Contract Negotiation messages, and Transfer Process messages. `dsp-connector` receives messages in both the Provider role and the Consumer role. `dsp-connector` checks the DAT bearer token on each request. `dsp-connector` checks that the caller owns the negotiation or transfer it asks about. `dsp-connector` converts internal error codes into the DSP spec's own error formats: `CatalogError`, `ContractNegotiationError`, and `TransferError`.

`dsp-connector` has no etcd client of its own. `dsp-connector` sends every data request to `catalog-service`, `negotiation-service`, or `transfer-process-service` over internal HTTP.

`dsp-connector` mounts every DSP endpoint under the path `/api/v1`. This matches the versioning convention already used elsewhere in DYNAMOS. The DSP spec's base-URL value is set by each dataspace member itself. The spec only fixes the path added after that base URL. So the `/api/v1` prefix stays fully compliant with the spec.

Two endpoints stay outside `/api/v1`: `/health` and `/.well-known/dspace-version`. `dsp-connector` also registers `/.well-known/dspace-version` a second time under `/api/v1`, because the TCK's metadata test group checks this path relative to whichever base URL the TCK is configured with.

**Catalog endpoints, Provider role, served by `dsp-connector`:**

- `POST /api/v1/catalog/request` — reads a Catalog Request Message. `dsp-connector` reads the caller's identity from the DAT. `dsp-connector` calls `catalog-service`. `dsp-connector` returns the catalog document, or one of three error responses: `403 not-provisioned` for an unknown participant, `502 upstream-error` for a `catalog-service` failure, or `401 missing-authorization` for a missing token.
- `GET /api/v1/catalog/datasets/{id}` — reads a Dataset Request Message.

**Contract Negotiation endpoints, Provider role, served by `dsp-connector`:**

- `POST /negotiations/request` — starts a negotiation. The new negotiation enters state `REQUESTED`. `dsp-connector` checks the offer ID against the requester's real catalog before it accepts the request. `negotiation-service` never performs this catalog check itself; `negotiation-service` stores state only.
- `GET /negotiations/{providerPid}` — reads the current state.
- `POST /negotiations/{providerPid}/request` — sends a counter-request. The negotiation returns to state `REQUESTED`. `dsp-connector` does not repeat the catalog check on a counter-request. The DSP spec accepts a counter-offer as a valid message on its own. The catalog check was an added DYNAMOS rule for the first request only.
- `POST /negotiations/{providerPid}/events` — accepts one event type: a Consumer-sent `ACCEPTED` event. `dsp-connector` rejects a Provider-sent `FINALIZED` event at this endpoint with HTTP status 400, because a Provider must not send this event to itself.
- `POST /negotiations/{providerPid}/agreement/verification` — moves the negotiation to state `VERIFIED`.
- `POST /negotiations/{providerPid}/termination` — moves the negotiation to state `TERMINATED`.

**Contract Negotiation endpoints, Consumer role, received by `dsp-connector` from a remote Provider:**

- `POST /api/v1/callback/negotiations/{consumerPid}/offers`
- `POST /api/v1/callback/negotiations/{consumerPid}/agreement`
- `POST /api/v1/callback/negotiations/{consumerPid}/events` (for a `FINALIZED` event)
- `POST /api/v1/callback/negotiations/{consumerPid}/termination`
- `POST /api/v1/negotiations/initiate` — starts a Consumer-role negotiation. This endpoint is a DYNAMOS addition, not part of the DSP spec.

`dsp-connector` checks negotiation ownership on every Provider-role endpoint except the initiating request. The DAT-verified caller must match the participant who created the negotiation. If the caller does not match, or if the `providerPid` does not exist, `dsp-connector` returns the same `404 not-found` response in both cases. This keeps the two cases indistinguishable, so a caller cannot use the response to find out which IDs are real.

**Transfer Process endpoints, Provider role, served by `dsp-connector`:**

- `POST /transfers/request`
- `GET /transfers/{providerPid}`
- `POST /transfers/{providerPid}/start` — accepted only as a Consumer-sent resume-after-suspend message; see the asymmetry note below.
- `POST /transfers/{providerPid}/completion`
- `POST /transfers/{providerPid}/termination`
- `POST /transfers/{providerPid}/suspension`
- `GET /transfers/{providerPid}/result` — the pull endpoint for a transferred result. A real external consumer's data plane calls this endpoint to fetch the data. `dsp-connector` checks the DAT and the ownership of this transfer, the same way it checks every other Provider-role endpoint. `dsp-connector` returns the stored job result, or `404 not-found` if no result exists yet.

`dsp-connector` validates the `agreementId` field on an incoming Transfer Request in two steps. First, `dsp-connector` tries to find a negotiation by `providerPid`, using the value of `agreementId` as that ID. This was DYNAMOS's original design, and DYNAMOS's own Consumer role and the TCK still use it. Second, if that lookup fails, `dsp-connector` looks up the negotiation by the Agreement's own `@id` field. Testing against a real, unmodified external DSP connector showed that this second method is what the DSP spec actually expects.

**Transfer Process endpoints, Consumer role, received by `dsp-connector`:**

- `POST /api/v1/callback/transfers/{consumerPid}/start`
- `POST /api/v1/callback/transfers/{consumerPid}/completion`
- `POST /api/v1/callback/transfers/{consumerPid}/suspension`
- `POST /api/v1/callback/transfers/{consumerPid}/termination`
- `POST /api/v1/transfers/initiate` — starts a Consumer-role transfer. This endpoint is a DYNAMOS addition, not part of the DSP spec.

**Identity and DAT verification.** Every DSP-facing endpoint requires an `Authorization: Bearer <token>` header. `dsp-connector` reads the `iss` claim from the token. This claim gives a `did:web` identifier. `dsp-connector` resolves this identifier to a public key over HTTP, using the pattern `did:web:{host%3Aport}:{path...}` → `https://{host}:{port}/{path...}/did.json`. `dsp-connector` checks the token's signature against this public key, using the ES256 or EdDSA algorithm.

`dsp-connector` uses the verified DID itself as the participant's identity. `dsp-connector` does not read an identity claim from an embedded credential. `dsp-connector` uses this DID as the lookup key in DYNAMOS's `Relations` map.

`dsp-connector` uses the `https` scheme for DID resolution in production. `dsp-connector` uses the `http` scheme for DID resolution in local and TCK builds, because these environments have no real TLS certificate. Eclipse EDC's own MVD makes the same relaxation for local testing.

This signature check proves one fact only: the caller holds the private key that matches the DID. This check does not implement the full DCP Presentation Flow. The full DCP Presentation Flow would also check a signed chain of issued credentials and a formal proof that the holder controls the credential. The DYNAMOS team left this out on purpose, as a known and accepted gap, not as an oversight.

### 2.4 negotiation-service

`negotiation-service` runs the DSP Contract Negotiation state machine. `negotiation-service` stores this state in etcd. `negotiation-service` is a separate service from `dsp-connector`, because a finalized negotiation must write into `policy-enforcer`'s etcd data. This write is a real integration point, and the DYNAMOS team judged it worth its own service.

**States.** `negotiation-service` has seven states: `REQUESTED`, `OFFERED`, `ACCEPTED`, `AGREED`, `VERIFIED`, and `FINALIZED`, plus the terminal state `TERMINATED`. A negotiation can reach `TERMINATED` from any other state. A negotiation can move between `REQUESTED` and `OFFERED` more than once, through a counter-request or a counter-offer, before it reaches `AGREED`.

**Negotiation record.** Each negotiation record has these fields:

- `Kind` — either `Provider` or `Consumer`. The default value is `Provider`.
- `ProviderPid` and `ConsumerPid` — formatted as `urn:dynamos:negotiation:{party}:{uuid}`. This format places the party name directly inside the ID.
- `Party` — the local party that owns this negotiation record.
- `Participant` — the other side's identity, either a DAT-verified DID or an email address, recorded when the negotiation starts. `dsp-connector` uses this field for ownership checks. `negotiation-service` uses this field for policy enforcement.
- `RemoteParticipant` and `ProviderEndpoint` — used only for a Consumer-role negotiation.
- `State` — the current state.
- `Offer` and `Agreement` — both stored as raw, unparsed JSON. `negotiation-service` does not read the meaning of the ODRL content inside these fields.
- `CallbackAddress` — the address `negotiation-service` sends outbound protocol messages to.
- Timestamps for record creation and state changes.

**Storage.** `negotiation-service` keeps an in-memory map of negotiation records, protected by a mutex. `negotiation-service` writes every change to etcd as well, and treats etcd as the source of truth. If a lookup misses the in-memory map, `negotiation-service` reads etcd instead. `negotiation-service` does not use etcd's `Watch` feature, so a cached record can go stale if another instance writes to the same key. The DYNAMOS team accepted this limit, because each party runs only one `negotiation-service` instance today, with no horizontal scaling.

`negotiation-service` stores Provider-role and Consumer-role negotiations under two separate etcd key prefixes. This keeps the two record sets from colliding.

`negotiation-service` also keeps a second etcd index. This index maps an Agreement's own `@id` field to the `providerPid` of the negotiation that owns it. The DYNAMOS team added this index after testing showed that a real external DSP consumer sends the Agreement's own `@id`, not the `providerPid`, on a Transfer Request.

**Internal API.** `negotiation-service` exposes one endpoint per DSP message type, under the path `/internal/v1/negotiations/...`. A parallel set of endpoints exists under `/internal/v1/negotiations/consumer/...` for the Consumer role. The endpoints are: create, read, counter-request, offer, events (`ACCEPTED` or `FINALIZED`), agreement, agreement verification, and termination.

**Policy enforcement.** When a negotiation reaches state `FINALIZED`, `negotiation-service` reads the negotiated Agreement and builds a DYNAMOS relation from it:

- the Agreement's `Target` field becomes the relation's `DataSets` field
- each `permission[].action` value, with the `dynamos:` prefix removed, becomes an entry in the relation's `RequestTypes` field
- the `dynamos:archetype` and `dynamos:computeProvider` constraints become the relation's `AllowedArchetypes` and `AllowedComputeProviders` fields

`negotiation-service` then writes this relation into the etcd key `/policyEnforcer/agreements/{party}`. `policy-enforcer` reads this same key in its existing, unchanged code. This write is the entire mechanism that gives a Consumer access to data after a negotiation finalizes. `policy-enforcer` needs no code change to support it.

`negotiation-service` writes this relation before it saves the `FINALIZED` state. If the write fails, `negotiation-service` leaves the negotiation at state `VERIFIED`. This stops a retry from wrongly reporting that access was already granted.

`negotiation-service` always reads the existing relation data before it writes. `negotiation-service` never overwrites the full etcd key blindly. The key `/policyEnforcer/agreements/{party}` also holds relation data for other consumers, so an overwrite would destroy that data.

**Outbound delivery.** `negotiation-service` sends Offer, Agreement, Termination, and `FINALIZED`-event messages to the counterparty's callback address. `negotiation-service` retries a failed delivery up to 5 times, with an exponential backoff starting at 250 milliseconds. The DYNAMOS team added this retry after TCK testing showed that a fast internal trigger can fire before the counterparty's own handler finishes registering. This produces a short-lived `404` response that is not a real rejection.

**Consumer-role autonomy.** `negotiation-service` includes two functions, `autoAcceptOffer` and `autoVerifyAgreement`, that accept every incoming offer or agreement for a Consumer-role negotiation. `negotiation-service` runs these functions in a separate goroutine, not directly inside the inbound handler. Testing showed that a direct, synchronous call-back to a Provider that is still waiting on its own HTTP response causes a connection reset. Running the call-back in a goroutine avoids this.

The environment variable `CONSUMER_AUTO_NEGOTIATE` controls this behavior. Its default value is `true`. The DYNAMOS team sets it to `false` only for the TCK's Consumer-role test group, because several of those tests need DYNAMOS to react differently to identical inbound messages. A single always-accept rule cannot pass those tests. When `CONSUMER_AUTO_NEGOTIATE` is `false`, four endpoints let an outside caller choose the reaction directly: accept, verify, counter, and terminate.

DYNAMOS does not yet run any real policy check for Consumer-role negotiation. The accept-all rule is a first step only. Real institutions such as UVA, VU, and SURF already sign real, legally binding consortium agreements outside of DSP entirely. The planned next step is a `dynamos:consortiumAgreementId` ODRL constraint. This constraint would turn the accept-all rule into an accept-if-known rule. The DYNAMOS team has not built this constraint yet.

### 2.5 transfer-process-service

`transfer-process-service` runs the DSP Transfer Process state machine. `transfer-process-service` is a separate service from `dsp-connector`, following the same split used for `negotiation-service`.

The DSP spec separates two layers: the control plane and the data plane. The control plane sends state-machine messages. `transfer-process-service` is the control plane. The data plane sends the actual data. DYNAMOS's existing job-execution pipeline — the gRPC and RabbitMQ chain, started through `api-gateway` — is the data plane. `transfer-process-service` does not change this pipeline.

**States.** `transfer-process-service` has five states: `REQUESTED`, `STARTED`, `COMPLETED`, `SUSPENDED`, and the terminal state `TERMINATED`. A transfer can move from `STARTED` to `SUSPENDED` and back to `STARTED`. A transfer has two possible terminal states: `COMPLETED` and `TERMINATED`.

**Start message asymmetry.** The Provider sends the first `REQUESTED → STARTED` message. `dsp-connector` sends this message outbound to the Consumer; no inbound endpoint exists for it. Only the resume case, `SUSPENDED → STARTED`, arrives as an inbound message from the Consumer, at `POST /transfers/{providerPid}/start`.

**Internal API.** `transfer-process-service` exposes endpoints under `/internal/v1/transfers/...`, plus a parallel set under `/internal/v1/transfers/consumer/...`. The endpoints cover create, read, start, completion, suspension, and termination.

**Job-execution wiring.** `transfer-process-service` connects a DSP transfer to a DYNAMOS job in these steps:

1. `transfer-process-service` starts the job in a separate goroutine, so a slow job does not delay the HTTP response to the Transfer Request Message.
2. `transfer-process-service` reads the job type from the `Format` field, after it removes the `dynamos:` prefix. For example, `dynamos:sqlDataRequest` becomes `sqlDataRequest`.
3. `transfer-process-service` reads the job body — the query, algorithm, columns, and options — from the `DataAddress` field. Neither the Offer nor the Agreement carries this information today, so `transfer-process-service` reuses the `DataAddress` field for it.
4. `transfer-process-service` sends this job as an `api.RequestApproval` message to `api-gateway`'s existing endpoint, `POST /api/v1/requestApproval`. This is the same endpoint the non-DSP `sqlDataRequest` flow already uses. `transfer-process-service` acts as an outside caller here, the same role `loadtest` and `sql-test` already play. `api-gateway`, `orchestrator`, `policy-enforcer`, and `agent` need no code change to support this.

If a Transfer Request carries an empty `DataAddress` field, `transfer-process-service` leaves the transfer at state `REQUESTED`. `transfer-process-service` does not auto-start and does not auto-terminate this transfer. An empty `DataAddress` field is a normal message from the TCK, or from any non-DYNAMOS consumer — not a failure. An earlier version of `transfer-process-service` auto-started every transfer with an empty `DataAddress` field. That version failed several TCK negative tests, because those tests check that a transfer stays at `REQUESTED` until a real follow-up message arrives. No information in the request alone tells the two cases apart, so `transfer-process-service` now stays passive by design.

`transfer-process-service` has one planned exception to this rule: the `defaultJobType` and `defaultJobRequest` settings. A party sets these through an environment variable; their default value is empty. When set, `transfer-process-service` runs a fixed, pre-set job for a transfer with an empty `DataAddress` field. This setting exists because a real external consumer has no way to build a DYNAMOS-shaped job request. [Section 6](#6-demo-setup-and-scripts) describes the demo script that uses this setting.

Once the job pipeline returns a result, `transfer-process-service` moves the transfer through `REQUESTED → STARTED → COMPLETED`. Before each state change, `transfer-process-service` reloads the transfer record from storage. A Consumer might have suspended or terminated the transfer in the meantime. If so, `transfer-process-service` logs this event and stops, instead of overwriting the Consumer's own state change.

The job result travels inside the Transfer Start Message's own `dataAddress` field. This is the one message in the state machine already built to carry a payload; the Transfer Completion message carries none.

The function `wireDataAddress` decides what value goes into this field on the wire:

- DYNAMOS's original design placed the raw job result directly into this field. This design works only between two DYNAMOS instances. A real external consumer's validation rejects this shape, because the spec expects a pull address, not the data itself.
- When a party sets the `connectorBaseURL` setting, `transfer-process-service` instead builds a pull address that points at `dsp-connector`'s own `GET /transfers/{providerPid}/result` endpoint. This endpoint checks the DAT and the ownership of the transfer, the same way every other Provider-role endpoint does. No separate token scheme was built for this pull step.
- When `connectorBaseURL` is empty (its default value), `transfer-process-service` uses the original, raw-result design. This keeps the DYNAMOS-to-DYNAMOS demo working without any change.

If the job call fails or times out, `transfer-process-service` moves the transfer directly to `TERMINATED`. `transfer-process-service` skips the `STARTED` state in this case, because the job never actually started.

**Fixed bugs.** The DYNAMOS team found and fixed three bugs in this area:

- `transfer-process-service` used to mark a transfer `COMPLETED` as soon as `api-gateway` sent its first acknowledgment, before the real job result arrived. The fix makes `transfer-process-service` wait for the real result.
- A job-routing function used to send every job in a chain to a hardcoded destination queue named for the first job only. Later jobs in the same chain never reached their correct queue. The fix removed this hardcoded value.
- The job-name generator used to require an "@" character in the identity string, matching an email address. A DID-based identity contains no "@" character, so this generator produced an empty job ID for any DSP-originated request. The fix removed this requirement.

### 2.6 Patterns Shared Across the Four Services

- **etcd is the only shared data store.** Each of the four DSP services keeps its own key range in etcd. One key range is shared on purpose: `negotiation-service` writes to `/policyEnforcer/agreements/{party}`, and both `catalog-service` and `policy-enforcer` read from it.
- **No direct use of RabbitMQ or gRPC.** None of the four DSP services call RabbitMQ or gRPC directly. `transfer-process-service` reaches DYNAMOS's RabbitMQ/gRPC job pipeline only indirectly, through its HTTP call to `api-gateway`.
- **No sidecar.** None of the four DSP services run a sidecar. This differs from DYNAMOS's normal chain-participant pattern.
- **Ownership checks.** `dsp-connector` checks that the DAT-verified caller matches the participant on record, on every endpoint except the first request. A mismatch and a missing record both return the same `404` response.
- **Outbound retry.** Both `negotiation-service` and `transfer-process-service` retry a failed outbound delivery up to 5 times, with exponential backoff starting at 250 milliseconds.
- **Async dispatch.** Any outbound call made from inside an inbound handler runs in a separate goroutine. This avoids blocking on a peer that is itself waiting on the current response.

---

## 3. Design Decisions

**DID-based identity, not a credential claim.** The DYNAMOS team first planned to read an email address from an embedded credential inside the DAT. Testing showed that MVD's issuer service supports only two fixed credential types, and adding a third type needs a code change outside the DYNAMOS repository. The DYNAMOS team decided instead to use the verified `did:web` identifier itself as the participant's identity, and as the key into the `Relations` map. This decision needs no external code change. The `Relations` map stays untyped: a non-DSP caller still uses an email address as its key, and a DSP-verified caller uses a DID as its key. DYNAMOS links no email-keyed entry to any DID-keyed entry automatically; an operator who wants that link must set it up by hand.

**A simulated dataspace built on MVD.** DYNAMOS's three per-party `dsp-connector` instances — for `UVA`, `VU`, and `SURF` — become real dataspace members by using MVD as an identity and trust layer only. MVD supplies an IdentityHub for each party, an Issuer Service as a trust anchor, `did:web` resolution, and the DCP protocol. DYNAMOS does not use MVD's own sample connector; DYNAMOS's own `dsp-connector` fills that role instead. This setup runs in its own separate cluster, apart from DYNAMOS's main cluster. This separation avoids two problems: a conflict between MVD's own ingress controller (Traefik) and DYNAMOS's ingress controller (nginx), and a mix of identity-layer changes with the rest of the DYNAMOS system. The DYNAMOS team judged that building DID, DCP, and credential cryptography from scratch was out of scope for this work. MVD is an official reference implementation, built for exactly this kind of conformance testing. Using MVD does not make any claim that DYNAMOS is a member of a production dataspace.

**The DSP layer must stay additive.** Every DSP-related change must add to DYNAMOS, not change DYNAMOS. New files, new packages, and new Helm charts are the preferred method. `catalog-service` and `dsp-connector`, each a new standalone service, set this pattern; `negotiation-service` and `transfer-process-service` followed it. When a shared file must change, the change must keep the same behavior for every existing, non-DSP caller. A developer must trace every caller of that file and confirm that none of them see a different result. If a developer cannot confirm this, the developer must stop and ask the project owner before proceeding.

Two real conflicts came up under this rule:

- An nginx ingress self-heal step touched the same ingress controller that `api-gateway` also uses.
- `orchestrator`'s reseed logic used to overwrite the full `/policyEnforcer/agreements/{party}` etcd key. This overwrite would have destroyed the same key that `negotiation-service` also writes to. The fix changed this overwrite into a merge. The DYNAMOS team then confirmed that the merged output matched the original overwrite output, byte for byte, for every existing caller.

This second conflict clarified the additive rule itself. The rule means every existing caller must see the same behavior. The rule does not mean zero lines of code may change inside a non-DSP file.

**Contract negotiation keeps state separate from meaning.** `negotiation-service` stores the `Offer` and `Agreement` fields as raw JSON. `negotiation-service` never reads the ODRL meaning of these fields. Only the policy-enforcement code, described in [Section 2.4](#24-negotiation-service), reads an `Agreement` and converts it into a DYNAMOS relation. This finalized-negotiation-writes-a-relation step is DYNAMOS's answer to the question "what does data access mean once a negotiation finalizes." The DYNAMOS team confirmed this design by running a real `sqlDataRequest` immediately after a negotiation finalized, with no restart of `policy-enforcer`. The custom ODRL terms `dynamos:archetype` and `dynamos:computeProvider`, and the `dynamos:`-prefixed permission actions, reuse vocabulary already set by the catalog work. The DYNAMOS team did not invent a new vocabulary for negotiation.

**Consumer-role identity works at the institution level.** DSP negotiation identity applies to a data-steward party — `UVA`, `VU`, or `SURF` — not to an individual person. DYNAMOS agents run no real policy check for DSP negotiation today. The Consumer-role behavior is an accept-all rule, built first as infrastructure, with real decision logic planned for later. The next planned step is a consortium-agreement-reference constraint, tied to the real, offline, legally binding agreements that these institutions already sign outside of DSP.

**API paths stay under `/api/v1`.** The DSP spec lets each dataspace member set its own base URL. Because of this, `dsp-connector` can add the `/api/v1` prefix to its own base URL and stay fully spec-compliant, while matching DYNAMOS's existing internal versioning pattern.

**The TCK harness is committed, permanent tooling.** The configuration files, runner scripts, and auto-responder programs under `go/cmd/dsp-connector/tck/` stay in the repository. Only the run logs are excluded from version control, through a `.gitignore` rule; a developer regenerates these logs on each run. [Section 4](#4-tck-testing-and-ci) explains how this tooling connects to CI.

**Real external testing found real spec corrections.** Testing against a real, unmodified external DSP connector — not only the TCK, and not only DYNAMOS talking to itself — found three corrections to DYNAMOS's original design:

- A Transfer Request's `agreementId` field should resolve through the Agreement's own `@id` value. DYNAMOS's original design expected the owning negotiation's `providerPid` instead. `dsp-connector` now supports both: it tries the `@id` lookup, matching the real spec behavior, and falls back to the `providerPid` lookup, for backward compatibility with existing callers.
- A Transfer Start Message's `dataAddress` field must hold a real, pull-able endpoint address. DYNAMOS's original design placed the raw result directly in this field, which worked only between two DYNAMOS instances.
- The job-name generator must not assume an email-shaped identity, because a DID-based identity contains no "@" character.

The DYNAMOS team found each of these three issues by testing, not by design review alone.

---

## 4. TCK Testing and CI

### 4.1 What the TCK Is

The DSP Technology Compatibility Kit (TCK) is the Eclipse Dataspace Working Group's own conformance test suite for the Dataspace Protocol. The TCK ships as a Docker image, `eclipsedataspacetck/dsp-tck-runtime:latest`. The TCK image runs as a container. This container acts as a counterparty against a real connector under test. The TCK image sends real catalog requests, contract negotiation messages, and transfer process messages, and checks the responses against the DSP spec.

The TCK groups its tests into six named groups. Its own configuration file, `tck.properties`, maps these groups to three tags:

- **CAT** (Catalog) and **MET** (Metadata) share the tag `dsp-cat`.
- **CN** (Contract Negotiation, Provider role) and **CN_C** (Contract Negotiation, Consumer role) share the tag `dsp-cn`.
- **TP** (Transfer Process, Provider role) and **TP_C** (Transfer Process, Consumer role) share the tag `dsp-tp`.

`dsp-connector` implements Provider-role catalog, Provider-role negotiation, Provider-role transfer, and Consumer-role negotiation. `dsp-connector` does not implement a Consumer-role transfer initiator. Because of this, every test in the `TP_C` group fails with a `404` response. This is expected. This is not a defect.

### 4.2 The Local Harness: `go/cmd/dsp-connector/tck/`

This directory holds nine files:

- `tck.properties` — the TCK's own configuration file. The TCK mounts this file, read-only, into its container.
- `run-tck.sh` — the local test runner. This script checks that `dsp-connector` is running. This script serves the fixture DID document over HTTP. This script runs the TCK container. This script searches the resulting log for pass and fail lines, grouped by test group.
- `gen_fixture.go` — a one-time generator, marked with the `//go:build ignore` tag, run by hand. This program creates a key pair. This program writes a `did:web` document for a fixture identity. This program creates a long-lived, signed DAT and writes it directly into `tck.properties`. A developer reruns this program only if the fixture key pair changes.
- `ci_seed_baseline.go` — a CI-only substitute for the etcd data that a full local development cluster already provides through `orchestrator`'s own seeding step. This program seeds one dataset and one baseline `policy-enforcer` agreement.
- `seed_cn_datasets.go` — seeds one dataset entry for each Contract Negotiation Provider-role test case. `dsp-connector` ties one offer ID to one identity, so a dataset entry is the only value that can change between test cases.
- `seed_tp_negotiation.go` — seeds one negotiation record, already at state `FINALIZED`, directly into `negotiation-service`'s etcd data. Every Transfer Process test case reuses this one fixture negotiation.
- `tck_auto_responder.go` — watches the etcd key prefix used by negotiations. For each negotiation that matches a known test dataset, this program sends the scripted Provider-role reaction for that test: offer, accept, counter, terminate, or finalize. This program exists because `negotiation-service` has no autonomous Provider-role behavior; no state change happens unless an outside caller triggers it through the internal API.
- `tck_auto_responder_consumer.go` — the Consumer-role counterpart to `tck_auto_responder.go`, keyed by dataset ID. This program exists because several Consumer-role tests send identical inbound content but require different reactions: accept, counter-offer, or terminate. A single fixed rule cannot pass all of these tests.
- `ci-check-results.sh` — checks a raw TCK log against an allowlist file. Every test ID listed in the allowlist file must appear in the log as a success. If any listed test ID is missing, this script exits with a non-zero status. A test ID that is not on the allowlist is never checked, in either direction.
- `ci-expected-passing.txt` — the current allowlist. It lists the full `CAT`, `MET`, `CN`, and `CN_C` groups, plus two Transfer Process tests: `TP:03-01` and `TP:03-02`.

Two behaviors of this harness are worth knowing before a developer debugs a CI failure:

- The environment variable `CONSUMER_AUTO_NEGOTIATE=false`, set on `negotiation-service` during a TCK run, turns off the real accept-all Consumer-role rule. This lets `tck_auto_responder_consumer.go` control each test's reaction instead.
- Only `TP:03-01` and `TP:03-02` pass against a real connector. The other Transfer Process test cases hang, because they depend on internal TCK self-test logic that never runs against a real external connector. This is a known limit of the TCK harness itself, not a defect in `dsp-connector`.

### 4.3 CI Workflow: `.github/workflows/dsp-tck.yml`

This workflow runs on two triggers: a push to the `main` branch, and a pull request into `main` that is not a draft.

The job `dsp-tck-validation` has a 12-minute time limit and runs these steps in order:

1. Check out the repository. Set up Go.
2. Start a standalone etcd container, using the `quay.io/coreos/etcd` image. The three DSP services under test need only a reachable etcd instance, not a full Kubernetes cluster.
3. Seed etcd with the CI baseline data, the CN test datasets, and the TP fixture negotiation.
4. Start `catalog-service`, `negotiation-service` (with `CONSUMER_AUTO_NEGOTIATE=false`), `transfer-process-service`, and `dsp-connector`, using `go run -tags local`. Poll each service's `/health` endpoint until it responds.
5. Serve the fixture DID document over HTTP.
6. Start both auto-responder programs, `tck_auto_responder.go` and `tck_auto_responder_consumer.go`, in the background.
7. Run the TCK image, filtered to the `dsp-cat,dsp-cn` tags, with a 5-minute time limit. Save the output to a log file.
8. Run the TCK image a second time, filtered to the `dsp-tp` tag, in the background. Poll the log file for the success line for `TP:03-02`, which normally appears within a few seconds. Stop this container as soon as that line appears, instead of waiting for the other Transfer Process tests to time out. Apply a 90-second safety limit as a backstop.
9. Stop any leftover TCK containers. This step always runs, even if an earlier step failed.
10. Check the combined log against the allowlist file, `ci-expected-passing.txt`. The job fails if any listed test ID is missing from the log.
11. Upload the raw TCK log as a build artifact. This step always runs, even if an earlier step failed.

The most recent change to this workflow added step 8 and the two Transfer Process lines in the allowlist file. Before this change, CI ran only the Catalog and Contract Negotiation groups. CI now also runs the Transfer Process group, filtered to the two tests known to pass against a real connector.

A passing CI run means the log matches the allowlist file, `ci-expected-passing.txt`. A passing CI run does not depend on the TCK image's own exit code. A test ID outside the allowlist file is never checked. This design lets known-hanging or out-of-scope tests exist alongside a real, enforced CI gate, because the gate only checks tests the team has already reviewed and accepted.

### 4.4 CI Workflow: `.github/workflows/backend-tests.yml`

This workflow runs on the same two triggers as `dsp-tck.yml`. This workflow is a separate file, because GitHub Actions has no way to share trigger settings between files. This workflow checks a separate concern: it builds and tests the Go code. It starts no DSP containers.

The job `synthetic-tests` runs in the `go` directory and runs these steps in order:

1. `go build`, excluding the `cmd/archetype_playground` package. This package contains a file of plain pseudocode, not valid Go code, which would otherwise break the whole build.
2. `go vet`, with the same exclusion.
3. `go test`, skipping two known, pre-existing test failures unrelated to the DSP work: a mock-type mismatch, and a test whose result depends on map iteration order.
4. `go build -tags local`, run only on the three services that support this build tag: `dsp-connector`, `negotiation-service`, and `catalog-service`. This step does not run on the whole repository, because not every service supports the `local` tag.
5. `go vet -tags local`, on the same three packages.
6. `go test -tags local`, on the same three packages, skipping one known, pre-existing failure. That failure checks for an `https` URL scheme, but the `local` build tag uses `http` instead.

The DYNAMOS team split these two workflows on purpose. The `backend-tests.yml` workflow finishes in about one minute and answers one question: does the code build and pass its unit tests. The `dsp-tck.yml` workflow takes a few minutes longer and answers a different question: does the live DSP protocol surface still conform to the spec.

### 4.5 Working with the TCK Locally

**To run a full local TCK pass:** start etcd, either as a standalone container or as part of the full local development cluster. Start the four services with `DATA_STEWARD_NAME=VU` set. Add `CONSUMER_AUTO_NEGOTIATE=false` on `negotiation-service` if testing the Consumer-role group specifically. Seed etcd with `seed_cn_datasets.go` and `seed_tp_negotiation.go`. Start both auto-responder programs. Then run `./run-tck.sh` from the directory `go/cmd/dsp-connector/tck/`.

**To reproduce a CI failure locally:** use the standalone-etcd setup, because this setup matches CI exactly. Start etcd directly with `docker run`. Run `ci_seed_baseline.go` first. Then follow the same seed, service-start, and auto-responder steps as a full local pass. Run `./run-tck.sh`. Check the resulting log against `ci-expected-passing.txt` with `ci-check-results.sh`.

**To read a result log:** search the log for lines that start with `SUCCESSFUL:` or `FAILED:`, followed by a test ID. Sort and count these lines to get a quick pass/fail summary for each test.

**Common problem:** a TCK container left running from an earlier local run. Check for a running container with this name before starting a new run.

No separate runbook document exists beyond this section and the comments inside the harness scripts themselves. The state-machine reference documents live under `docs/negotiation/`, `docs/catalog/`, and `docs/transfer/`.

---

## 5. MVD Interaction

### 5.1 What MVD Is

The Minimum Viable Dataspace (MVD) is Eclipse EDC's own reference implementation of a small, working dataspace. The MVD repository is `eclipse-edc/MinimumViableDataspace`. MVD provides an IdentityHub for each participant, an Issuer Service as a trust anchor, sample control-plane and data-plane connectors, `did:web` resolution, and the Decentralized Claims Protocol (DCP). MVD is not production software. MVD exists to help a team check that its own connector implementation works correctly against a real, independent implementation.

DYNAMOS uses MVD in two ways:

- **As an identity and trust layer for DYNAMOS's own multi-party setup.** DYNAMOS's `dsp-connector` instances for `VU`, `UVA`, and `SURF` receive DIDs and credentials from MVD's IdentityHub and Issuer Service. DYNAMOS does not use MVD's own sample connectors; DYNAMOS's own `dsp-connector` fills that role. This setup runs in its own separate cluster.
- **As a real external DSP peer.** MVD's own control plane, acting as a Consumer, connects directly to a real DYNAMOS `dsp-connector`, acting as a Provider. This setup proves that DYNAMOS works with an independent implementation, not only with itself.

A developer clones MVD as a sibling directory to this repository, named `dynamos-mvd`, at the path `../dynamos-mvd`. The devcontainer configuration references this path. The devcontainer installs a Java Development Kit (JDK), because building MVD from source requires it.

A developer who rebuilds MVD from scratch should know about four issues in advance:

- MVD's published container images reference an outdated registry name. This is a known issue in the upstream MVD project.
- A pre-built `latest` image from MVD is a multi-platform image. The `kind load docker-image` command can fail on a multi-platform image. A developer can work around this with a manual save-and-import step.
- MVD's IdentityHub never returns a participant's own private signing key. An MVD-issued identity can prove that it exists, but DYNAMOS cannot sign a message as that identity without MVD's own vault access.
- Building MVD from a tagged release, instead of the current tip of its main branch, avoids most of the issues above.

DYNAMOS avoids the private-key issue by using a self-hosted fixture identity for the TCK and for the DYNAMOS-to-DYNAMOS demo, described in [Section 6](#6-demo-setup-and-scripts). This fixture identity uses a key pair that DYNAMOS generates and controls itself. This fixture identity still goes through the same DAT-verification code as a real MVD identity.

### 5.2 How dsp-connector Talks to MVD

**Roles.** `dsp-connector` acts as Provider for catalog, negotiation, and transfer messages exchanged with MVD. MVD acts as Consumer, driven through its own Management API. `dsp-connector`'s Consumer-role negotiation code is exercised separately, by the TCK's Consumer-role test group and by the `dsp-transfer-demo.sh` script described in [Section 6.3](#63-dsp-transfer-demosh--dynamos-to-dynamos-demo).

**Identity.** `dsp-connector` uses the DID-based identity method described in [Section 2.3](#23-dsp-connector). Each party has a `did:web` identifier. `dsp-connector` resolves this identifier over HTTP to a document that holds the party's public key. `dsp-connector` verifies the signature on every inbound DAT against this key, and uses the verified DID as the participant's identity.

**Outbound authentication.** DYNAMOS calls MVD in one of two ways. Without extra configuration, DYNAMOS uses a static, long-lived, signed DAT. When a party configures an STS token URL, client ID, and client secret, DYNAMOS instead requests a fresh token for each outbound call, through an OAuth2 client-credentials request to MVD's own IdentityHub STS endpoint. DYNAMOS requests a fresh token for each call, because a real DCP verifier checks the token's audience field, and a cached token would fail this check for a new recipient.

**Catalog request.** MVD sends a real Catalog Request Message to DYNAMOS's DSP endpoint, through MVD's own Management API.

**Negotiation.** MVD starts a negotiation with a real, ODRL-formatted Contract Request. An operator drives `dsp-connector`'s Provider-role response directly, through `dsp-connector`'s own internal API. This response skips the formal offer-and-accept round trip. The negotiation moves directly from `REQUESTED` to `AGREED`. Testing showed that this direct path is more reliable than resending an identical offer, because a real EDC implementation only accepts a resend if it matches the original offer byte for byte. MVD then moves the negotiation from `AGREED` to `VERIFIED` on its own, with no outside trigger needed.

**Transfer.** MVD starts a real Transfer Request, using the transfer type `HttpData-PULL`. DYNAMOS runs its configured job. Once the job finishes, DYNAMOS sends a Transfer Start Message. This message's `dataAddress` field holds a real pull address, pointing back at `dsp-connector`'s own result endpoint, `GET /transfers/{providerPid}/result`. A real consumer's data plane should call this endpoint with its own STS-issued token to fetch the result. MVD's own data plane has a known defect that stops it from making this call on its own. [Section 6.4](#64-dsp-external-consumer-demosh--external-mvd-consumer-demo) describes the demo script that works around this defect, to prove that DYNAMOS's own provider side works correctly.

**Limit on custom queries.** A real external consumer has no way to know DYNAMOS's own job-request format. Because of this, `transfer-process-service` accepts only its configured default job for a transfer with no matching internal job spec. If MVD sends a custom, non-empty `dataDestination` value, `transfer-process-service` routes the transfer down a different, unsupported code path, and the transfer ends in state `TERMINATED`. This behavior is intentional. This behavior matches the same protocol-safety check the TCK's own negative tests require. This behavior is not a defect.

---

## 6. Demo Setup and Scripts

All demo scripts live under `configuration/demo/`. Two interactive, menu-driven scripts share one setup script. This document lists every script by name. No separate documentation page describes this flow; the comments inside each script are the primary source, summarized here.

### 6.1 The Two Interactive Demo Scripts

- **`dsp-transfer-demo.sh`** — a DYNAMOS-to-DYNAMOS demo. In this demo, the identity `VU` (Consumer role) negotiates with, and receives a transfer from, the identity `UVA` (Provider role). Both identities run inside the same local cluster. This demo uses a self-hosted fixture identity, not a real external party.
- **`dsp-external-consumer-demo.sh`** — an external-interoperability demo. In this demo, MVD (described in [Section 5](#5-mvd-interaction)) runs in a second, separate local cluster and acts as Consumer. A real DYNAMOS `dsp-connector`, for party `VU`, acts as Provider.

Both local clusters connect through the same Docker bridge network. This connection lets host aliases and NodePort services bridge traffic between the two clusters, even though a Kubernetes Service is normally reachable only inside its own cluster.

### 6.2 `setup-demos.sh` — Shared Setup Script

`setup-demos.sh` is the one setup script both demo scripts need. `setup-demos.sh` is safe to run more than once. A developer runs `setup-demos.sh` once, and again after any full teardown of either cluster. `setup-demos.sh` runs five steps in order:

1. **Check required tools.** `setup-demos.sh` installs any missing command-line tools.
2. **Set up the MVD cluster.** If the MVD cluster does not exist, `setup-demos.sh` creates it. `setup-demos.sh` installs MVD's ingress controller and Gateway API resources. `setup-demos.sh` builds MVD's own application images from source, if these images do not already exist. `setup-demos.sh` loads these images into the MVD cluster. `setup-demos.sh` applies MVD's own Kubernetes manifests. `setup-demos.sh` waits for every MVD deployment and seed job to finish. One seed job is known to leave stale data behind after a partial failure; `setup-demos.sh` retries this job once.
3. **Re-seed MVD's identity data, if needed.** MVD's own database and vault use no persistent storage. A pod restart erases this data, even though the seed job still reports success. `setup-demos.sh` checks whether this data still exists, and re-seeds it if not. `setup-demos.sh` also creates, or reads, the `VU` party's real MVD identity.
4. **Redeploy DYNAMOS and connect it to MVD.** `setup-demos.sh` starts the local DYNAMOS cluster if it is stopped. `setup-demos.sh` re-exports the cluster's connection settings, because the cluster's API server port can change after a stop and restart. `setup-demos.sh` reinstalls cluster DNS if a teardown removed it. `setup-demos.sh` clears any proxy pods stuck in a crash loop from a stale network sandbox. `setup-demos.sh` then runs `dynamos-configuration.sh`, described below.

   After this deployment finishes, `setup-demos.sh` connects the `VU` party's DYNAMOS deployment to its real MVD identity. `setup-demos.sh` creates a Kubernetes secret holding the `VU` party's STS credentials. `setup-demos.sh` creates a config map holding a default job request. `setup-demos.sh` sets these values as environment variables on `negotiation-service-vu` and `transfer-process-service-vu`, and restarts these two deployments if their secret changed. `setup-demos.sh` adds host aliases so DYNAMOS pods can resolve MVD's internal DNS names. `setup-demos.sh` exposes `dsp-connector-vu` through a NodePort service. `setup-demos.sh` seeds a policy-enforcer agreement in etcd, giving the MVD consumer identity access to the `VU` party's data. `setup-demos.sh` also adds host aliases on the MVD side, so MVD can resolve DYNAMOS's own DSP endpoint.

5. **Set up extra fixtures for the internal demo.** `setup-demos.sh` creates a fixture DID identity, if it does not already exist. This fixture identity is an nginx pod that serves a DID document from a config map. `setup-demos.sh` sets the `UVA` and `VU` deployments to use a specific branch-build image tag, because the current default branch tag does not yet include certain job-name fixes.

`setup-demos.sh` calls `dynamos-configuration.sh` as part of step 4. `dynamos-configuration.sh` is the core DYNAMOS deployment script. `dynamos-configuration.sh` runs these steps:

- confirms the correct cluster is the active `kubectl` context
- creates a fresh, random password for RabbitMQ
- writes this password into RabbitMQ's configuration, and pushes this configuration into RabbitMQ's persistent volume through a temporary pod, using the script `fill-rabbit-pvc.sh`
- installs or upgrades Prometheus, the ingress controller, the Gateway API resources, and the service mesh
- installs the `core` Helm chart, which includes etcd, RabbitMQ, and the observability stack
- waits for RabbitMQ to start, then updates RabbitMQ's internal user password to match the new secret
- installs each remaining Helm chart in order: `orchestrator`, `agents`, `catalog-service`, `negotiation-service`, `transfer-process-service`, `dsp-connector`, `thirdparty`, and `api-gateway`

### 6.3 `dsp-transfer-demo.sh` — DYNAMOS-to-DYNAMOS Demo

A developer must run `setup-demos.sh` at least once before running `dsp-transfer-demo.sh`. On each run, `dsp-transfer-demo.sh` performs five steps automatically:

1. `dsp-transfer-demo.sh` checks that required tools are present and that the cluster is healthy. `dsp-transfer-demo.sh` opens the port-forwards it needs, to the ingress controller, to both `negotiation-service` instances, and to etcd.
2. `dsp-transfer-demo.sh` creates a fresh demo identity and a fresh DAT token. `dsp-transfer-demo.sh` creates the fixture DID identity first, if it does not already exist.
3. `dsp-transfer-demo.sh` sets the new DAT token as an environment variable on all four DSP services that need it, and waits for these services to restart.
4. `dsp-transfer-demo.sh` writes a policy-enforcer relation for the fixture identity into etcd. `dsp-transfer-demo.sh` also writes a demo dataset entry into etcd.
5. `dsp-transfer-demo.sh` opens an interactive menu. From this menu, a developer can request the catalog, drive the negotiation state machine step by step (initiate, offer, agreement, finalize), and drive a transfer (choose a dataset or a query, initiate the transfer, poll until it completes).

Closing this menu stops only `dsp-transfer-demo.sh`'s own port-forwards. Closing this menu does not stop the cluster. This lets a developer run `dsp-transfer-demo.sh` again without repeating cluster setup.

### 6.4 `dsp-external-consumer-demo.sh` — External MVD Consumer Demo

A developer must run `setup-demos.sh` before running `dsp-external-consumer-demo.sh`. `dsp-external-consumer-demo.sh` has no setup step of its own. On each run, `dsp-external-consumer-demo.sh` performs two steps:

1. `dsp-external-consumer-demo.sh` opens six port-forward bridges between the two clusters: MVD's Management API, MVD's DSP protocol port, MVD's STS token endpoint, MVD's DID-serving port, and DYNAMOS's own `negotiation-service` and `transfer-process-service` internal APIs.
2. `dsp-external-consumer-demo.sh` opens an interactive menu. This menu drives the flow through MVD's real Management API, not through fixtures. A developer can request the catalog from MVD, start a negotiation from MVD and finalize it by calling DYNAMOS's internal API directly, start a transfer from MVD and poll until it completes, and finally pull the transferred data directly. This last step stands in for MVD's own data plane, which has a known defect that stops it from completing this pull on its own. This step proves that DYNAMOS's own provider side works correctly, end to end.

`dsp-external-consumer-demo.sh` exercises only one fixed dataset. `dsp-transfer-demo.sh` supports more than one dataset and a custom query; `dsp-external-consumer-demo.sh` does not, because a real external consumer has no way to choose a custom query.

### 6.5 `mint-identity.sh` and the Postman Collection

`mint-identity.sh` and a Postman collection together offer a manual, step-by-step alternative to the `dsp-transfer-demo.sh` menu.

- `mint-identity.sh` creates a fresh, signed identity and DAT token. `mint-identity.sh` publishes this identity's DID document. `mint-identity.sh` prints the DAT token, for a developer to paste into a Postman environment variable.
- The Postman collection, `DYNAMOS-DSP-Demo.postman_collection.json`, groups its requests into folders that match the protocol flow in order: an identity check, a catalog request, the full six-step negotiation state machine, and one bonus data-request call.

### 6.6 `dynamos-helpers.sh`

`dynamos-helpers.sh` is a separate library of shell functions for manual, per-chart deployment and cluster housekeeping. `dynamos-helpers.sh` offers functions to install or remove one chart at a time, to install or remove groups of charts, and to run a full, ordered redeploy with fixed delays between steps, to avoid startup-ordering problems.

`dynamos-helpers.sh` predates the demo scripts described above. No demo script calls `dynamos-helpers.sh`. `dynamos-helpers.sh` also uses a different repository-root path convention than the other scripts, and includes cleanup functions written for one specific developer's own stale jobs. A developer should treat `dynamos-helpers.sh` as a separate, manual tool, not as part of the demo run order below.

### 6.7 Run Order

1. Clone MVD as a sibling directory, `../dynamos-mvd`, if running `dsp-external-consumer-demo.sh`.
2. Run `configuration/demo/setup-demos.sh` once, or again after a full teardown.
3. Run `configuration/demo/dsp-transfer-demo.sh`, or `configuration/demo/dsp-external-consumer-demo.sh`. Both scripts are safe to run again without repeating setup.
4. As an optional alternative, run `mint-identity.sh` and use the Postman collection instead of the `dsp-transfer-demo.sh` menu.

---

## 7. Code Review Findings

> **A large language model (LLM) was used to produced this section**, from an automated code review of the codebase. The review covered the Go services, the shared Go libraries, the Python services, and the Kubernetes/Helm deployment files. The review did not cover the web frontend. Every finding below names a real file and location that the review read directly

### 7.1 Summary

| Severity  | Count   |
| --------- | ------- |
| Critical  | 18      |
| High      | 47      |
| Medium    | 46      |
| Low       | 27      |
| **Total** | **138** |

### 7.2 Top Risks

1. **The non-DSP authentication path accepts any token.** `go/cmd/policy-enforcer/generate_validation_response.go:55-60` always returns the fixed token `"1234"`. `go/cmd/api-gateway/requests.go:228-231` always sends `"bearer 1234"`. `go/cmd/agent/token_validation.go:25-29` reads a bearer token but never checks it. A caller can send any token value and pass this check.
2. **`orchestrator`'s write API has no authentication.** `go/cmd/orchestrator/api.go:62-88` — any caller that can reach this API can overwrite agreements, policy data, and archetype configuration directly in etcd, and can trigger a full configuration reload.
3. **RabbitMQ ships with default credentials, reachable from outside the cluster.** `charts/core/values.yaml:9-12,10`, `configuration/k8s_service_files/rabbitmq.conf:8`, and `charts/core/templates/rabbitmq-services.yaml:9-24` set the default login `guest`/`guest`, a fixed Erlang cookie, and remote guest login, all committed in plain text, exposed through a NodePort service. A caller with network access has full administrative control of the message broker that every service depends on.
4. **No NetworkPolicy resource exists in any Helm chart.** DYNAMOS's design separates each data-steward party into its own namespace. Without a NetworkPolicy, any pod in any namespace can reach any pod in any other namespace over the pod network. This makes the namespace separation a naming convention only, not a real network boundary.
5. **The federated SQL query service runs an untrusted query string directly.** `python/sql-query/main.py:84` passes the caller's query string straight to SQLite through pandasql, with no check that limits the query to `SELECT` statements, no parameterization, and no complexity limit. This is the core function of this service, and also its core risk.
6. **The sidecar service treats a normal runtime error as a fatal error.** `go/cmd/sidecar/grpc_server.go:63-66,73-76` and `go/cmd/sidecar/rabbit_setup_functions.go:42,50,68,74,95` call `Fatalw`, which stops the whole process, on a RabbitMQ error that can happen during normal operation, not only at startup. A single transient RabbitMQ error stops the entire sidecar pod.
7. **Three services crash on one malformed queue message.** `go/cmd/api-gateway/consume.go:29`, `go/cmd/orchestrator/consume.go:26,34`, and `go/cmd/policy-enforcer/consume.go:22,36` each call `Fatalf` when a queue message fails to parse. A single bad message can stop any of these three services. This is a simple, repeatable denial-of-service path.
8. **This same fatal-on-error pattern repeats across the shared libraries.** `go/pkg/etcd/*.go` (at least 6 locations), `go/pkg/lib/consume.go:28-57`, `go/pkg/lib/go-grpc.go`, `go/pkg/lib/grpc_server.go:27`, and `go/pkg/msinit/main.go` all call `Fatalw` or `Fatalf` on errors that a normal request can trigger, not only on startup errors. Because these are shared library files, this pattern affects every service that imports them.
9. **A message-format mismatch breaks the Python message-forwarding path.** `python/dynamos-python-lib/dynamos/grpc_server.py:93-122` always returns an `Empty()` message. The protocol definition, and the Go code that calls it at `go/cmd/sidecar/rabbit_ms_chain.go:162`, both expect a `ContinueReceiving` message instead. This mismatch breaks the response for every Python microservice.
10. **The sidecar service has an unprotected shared counter.** `go/cmd/sidecar/rabbit_send.go:58,134,137` — more than one goroutine changes a package-level counter with no lock. This counter also controls when the sidecar shuts down. This can close the AMQP channel while a message is still sending, or stop the shutdown process from completing.
11. **A failed message publish is reported as a success.** `go/cmd/sidecar/ms_communication_handlers.go:78-82` — the line that returns the real error is commented out. The code returns a fixed success value instead.
12. **Raw dataset files are built into a Docker image that gets pushed to a registry.** `python/Dockerfile:18` copies the raw CSV data files directly into the image. DYNAMOS's design keeps each data steward's data with that data steward. Placing the raw data files inside a pushed image works against this design.

### 7.3 Cross-Cutting and Architecture Findings

- Every HTTP-serving service under `go/cmd/` (7 services) starts its HTTP server with `http.ListenAndServe` and sets no read timeout, no write timeout, no idle timeout, and no graceful shutdown handler. A slow client connection can hold a connection open indefinitely. A pod shutdown stops in-flight requests instead of finishing them. Severity: High.
- `go/cmd/negotiation-service/policy_enforcement.go:111-134` and `go/cmd/orchestrator/etcd_config.go:72-83` both read, then write, the same etcd key with no transaction. Two negotiations that finalize for the same party at the same time can overwrite each other's data. Severity: High.
- Every legacy HTTP service sets a wildcard CORS origin and allows the `Authorization` header. Combined with finding 1 above, a request from any origin can reach these services with a credential that these services never actually check. Severity: Medium.
- `catalog-service`, `negotiation-service`, and `transfer-process-service` check no application-level credential on their internal APIs. These services rely on network isolation alone. Severity: Medium.
- Each of the shared packages `api`, `etcd`, `lib`, `mschain`, and `msinit` builds its own separate logger, through its own local/production build-tag pair, instead of sharing one logger. One package skips this split entirely. One package has the split, but its production setting does not lower the log level as intended. Severity: Medium.
- The `.proto` files carry no version number and no automated check for a breaking change. Severity: Medium.
- All 10 generated Go files under `go/pkg/proto/` import an older, deprecated compatibility package instead of the current package. Severity: Medium.
- `go.opencensus.io` and `github.com/gogo/protobuf`, two direct dependencies used across most services, are both archived by their maintainers and receive no further security updates. Severity: Low.

### 7.4 Go Core Services (`go/cmd/`)

The four DSP services — `catalog-service`, `dsp-connector`, `negotiation-service`, `transfer-process-service` — show stronger production practice throughout this review: real authentication, documented design tradeoffs, and test coverage. The findings below concentrate in the older, non-DSP services.

**agent**

- `go/cmd/agent/sql_request_handler.go:230-238` — after a deployment failure, the code continues and reads a field from a job object that is `nil`. This causes a guaranteed crash. Severity: High.
- `go/cmd/agent/etcd.go:29-40` — a fixed, 5-second retry loop works around an etcd timing issue. This adds latency to every SQL request on the normal path. Severity: Medium.
- `go/cmd/agent/main.go:37` and `go/cmd/agent/incoming_microservice_communication.go:37` — each incoming request starts a new goroutine, with no limit on the number of goroutines and no recovery from a panic inside one. Severity: Medium.
- `go/cmd/agent/register_agent.go:32,46` — the registration and heartbeat calls discard their error value. A failure here stays invisible. Severity: Medium.
- `go/cmd/agent/token_validation.go:25-36` — this code logs the bearer token at debug level, then never checks it. Severity: Medium.
- `go/cmd/agent/config_prod.go:20` — this file contains one developer's own local file path. Severity: Low.

**api-gateway**

- `go/cmd/api-gateway/config_local.go` — this file is missing required configuration fields. A build with the `local` tag fails. CI already excludes this build as a known gap. Severity: Medium.
- `go/cmd/api-gateway/requests.go:169-216` — this code starts one goroutine per provider, with no limit and no explicit timeout. Severity: Low.

**catalog-service** — this review found no independent issues beyond the shared internal-authentication finding above. This service has clear error handling and test coverage.

**dsp-connector**

- `go/cmd/dsp-connector/dat_verification.go:222-227` — this code does not require a token expiry claim. The DYNAMOS team already documented this gap as an accepted, deferred item. Severity: Low.
- Files under `tck/` and `demo/` call `panic()` in several places. This is acceptable in one-shot scripts. A developer should confirm that these programs never run as long-lived services. Severity: Low.

**negotiation-service**

- `go/cmd/negotiation-service/auto_accept.go:133-170` — this code accepts every incoming contract offer, with no real policy check. The DYNAMOS team tracks this as an intentional first step, described in [Section 3](#3-design-decisions). This is still a real risk in a production deployment as it stands today. Severity: High.

**orchestrator**

- `go/cmd/orchestrator/etcd_config.go:102` — a format string for an etcd key contains one extra space character before the closing quote. A lookup that uses the clean key name will not find this key. Severity: Medium.
- `go/cmd/orchestrator/manage_jobs.go` (362 lines) and `go/cmd/orchestrator/composition_request.go` — this file mixes job lifecycle logic, policy re-evaluation, and archetype selection in one place. This file builds etcd key strings by hand, in slightly different argument orders, in about 8 separate locations. The extra-space bug above is a direct result of this pattern. Severity: Medium.
- Two `TODO` comments in `handleRequestApproval` mark incomplete error-reporting code. Severity: Low.

**policy-enforcer**

- `go/cmd/policy-enforcer/policy_update.go:23-37` — this code calls `SendPolicyUpdate` twice for the no-agreements case. Severity: Low.

**sidecar**

- `go/cmd/sidecar/rabbit_send.go:67` — this code registers a new listener on every message publish, and never removes an old listener. Memory use grows without limit as message volume grows. Severity: High.
- `go/cmd/sidecar/rabbit_ms_chain.go:41` — this code stores a cancel function but never calls it. This leaks a goroutine and a context value on every chain stop. Severity: Medium.
- `go/cmd/sidecar/main.go:62-63` — this code waits a fixed 4 seconds before shutdown, instead of waiting for a real completion signal. Severity: Low.
- `go/cmd/sidecar/rabbit_setup_functions.go:79` — the exchange name is written directly into this file in several places, with no configuration option. Severity: Low.

**transfer-process-service** — this review found no major issues beyond the shared findings above. This service has a clear state machine and documented design tradeoffs.

_*archetype_playground, loadtest, and the sql-* tools_*

- `go/cmd/archetype_playground/pseudocode.go` — this file contains plain pseudocode, not valid Go code. This file breaks a repository-wide build or `vet` check unless a developer excludes it. CI already excludes this file. Severity: Medium.
- `go/cmd/sql-algorithm/application_logic.go:178-229` and `go/cmd/sql-anonymize/application_logic.go:44` — these files contain fixed column names written directly into the code. These services are meant to work with more than one dataset, but these fixed names limit each service to one demo dataset's structure. Severity: Medium.
- `go/cmd/loadtest/main.go:41,221` — this file uses the same fixed `"bearer 1234"` token used elsewhere, and contains large blocks of commented-out code. Severity: Low.
- `go/cmd/sql-test/main.go:103-142` — this file's `main` function is mostly commented-out code, left in the repository as if it were a working service. Severity: Low.
- `go/cmd/sql-aggregate/application_logic.go:1-9` — this file starts with unedited planning notes above the code itself. Severity: Low.

**Test coverage** — `agent`, `api-gateway`, `orchestrator`, `sidecar`, and every `sql-*`, `loadtest`, and `archetype_playground` package contain no test files. These are the same packages that carry the fixed-token authentication issue, the fatal-on-error pattern, and the sidecar race condition described above. Severity: High.

### 7.5 Go Shared Libraries (`go/pkg/` and `proto-files/`)

**pkg/api**

- `go/pkg/api/http.go:148` — this handler calls `Fatalw` when a marshal step fails on data read from etcd. One malformed record stops the whole service, instead of returning an HTTP 500 response. Severity: Critical.
- `go/pkg/api/http.go:170-222` — this code writes a caller-supplied name value directly into an etcd key path, with no validation on that value. Severity: High.
- `go/pkg/api/http.go:243` — the outbound HTTP client used here sets no timeout and carries no context value. Severity: High.
- `go/pkg/api/http.go:14-16,127,170` — these handlers depend directly on one concrete etcd client type, even though this package already defines interface types built for this purpose. Severity: High.
- No test file covers the shared CRUD path that every service's configuration data passes through. Severity: High.

**pkg/etcd**

- `go/pkg/etcd/types.go:12`, `go/pkg/etcd/etcd_put.go:68`, and `go/pkg/etcd/etcd_lease.go:24,29` — this package keeps one shared, package-level map with no lock. Every importing service writes to this map from its own request-handling goroutines. Severity: Critical.
- `go/pkg/etcd/etcd_lease.go:23-25` — this function calls a `nil` function value when the caller passes a key not present in the map. This function is also never called anywhere in the repository. Severity: High.
- `go/pkg/etcd/etcd.go:58-68` — the etcd client used by every service sets no TLS option and no authentication option. Severity: High.
- `go/pkg/etcd/etcd_get.go:58-115` — this function treats a "key not found" result as a retryable error. A normal, empty result can take up to 30 seconds to return. Severity: High.
- Only one function in this package has any test coverage. Severity: High.
- Two further medium findings cover fragile, undocumented behavior: reliance on a specific pointer-allocation pattern in `encoding/json`, and a logged error that is not returned to the caller as a real error value.

**pkg/lib**

- `go/pkg/lib/consume.go:28-57` — this function calls `Fatalf` on any handler error. This makes the retry loop this function is meant to protect unreachable in exactly the case it was built for. This function sits at the center of every service's message-consumption path. Severity: Critical.
- `go/pkg/lib/go-grpc.go:35-50` — a loop-bound error causes the fatal check at the end of this function to never run. After the retry count runs out, this function returns a connection with no error, even though the connection never reported as ready. Severity: High.
- `go/pkg/lib/go-grpc.go:24` — every gRPC connection built through this function uses plain, unencrypted transport. Severity: High.
- `go/pkg/lib/grpc_server.go:46-53` — malformed trace data, sent by another service, can cause this function to call a method on a `nil` span value. Severity: High.
- `go/pkg/lib/grpc_server.go:23-31` — this function is a live, network-reachable RPC. Each call to it creates a new exporter connection and never closes the previous one. Severity: High.
- `go/pkg/lib/tracing.go:24` — this file sets an insecure transport option for the trace exporter. A comment inside the file notes that this setting should change before production use; this change was never made. Severity: High.
- No test file covers the connection-retry logic, the gRPC server setup, the tracing code, or the RabbitMQ consume-retry loop in this package. Severity: High.

**pkg/mschain and pkg/msinit**

- `go/pkg/msinit/main.go:194-211` — this function uses an unbuffered channel. If a timeout fires first, the goroutine's later send on this channel has no receiver, and that goroutine never finishes. Severity: High.
- `go/pkg/msinit/main.go:115` — this line discards both return values from a chain-initialization call. A failure here looks the same as a success. Severity: High.
- `go/pkg/mschain` has no test file. This package's ordering and cycle-detection logic supports every archetype and composition request. Severity: High.
- A smaller set of medium and low findings cover an inconsistent debug-logging default between packages, duplicated logic instead of a shared function, a fixed shutdown delay, and unused code.

**proto-files and pkg/proto**

- The RabbitMQ protocol file defines two RPCs with the same request and response types and near-identical names. Only one of the two has any caller in the repository. Severity: Medium.
- The RabbitMQ protocol file also defines a test RPC that ships as part of the production protocol contract, with no caller anywhere in the repository. Severity: Medium.
- The core inter-service message type carries untyped payload fields, with no schema check at the protocol level. Severity: Medium.
- A tracing field definition repeats, word for word, across three separate `.proto` files, instead of one shared definition reused through an import. Severity: Low.

### 7.6 Python Services (`python/`)

- The SQL query service runs an untrusted query directly, described in [Top Risks](#72-top-risks) above.
- No gRPC client call in this codebase sets a timeout: not the health check, not `SendData`, not the chain start or stop calls. A fixed retry loop with no backoff, and no code that catches its final exception, can crash a service at startup if a dependency starts slowly. Severity: High.
- The gRPC server here sets no TLS option and checks no application-level credential. Any caller that can reach this service's network address can call it with any payload. Severity: High.
- Two dependency versions in this codebase have known security fixes available: `grpcio==1.59.3` and `protobuf==4.25.3`. Severity: High.
- A failed query passes through a chain of increasingly broad exception handlers. The caller never receives a response or an error message. A federated query can fail with no visible sign of failure. Severity: High.
- The `sql-test` service opens a network connection as a side effect of its own import step, and would raise a type error if a developer ever ran it. This service is not referenced by any deployment file. Severity: Medium.
- The Dockerfile for this codebase sets no `USER` instruction, so the container runs as the root user. The Dockerfile also pins no digest for its base image, and includes one step that deliberately replaces the pinned pandas and numpy versions with newer, unpinned ones. Severity: Medium.
- No automated test exists anywhere under `python/`, and no CI workflow runs a Python lint or test step. This gap covers the highest-risk code in this review: the code that runs an untrusted query. Severity: Medium to High.
- A set of lower-severity findings: stale generated protocol files that still reference an old module path, developer file paths written directly into configuration files, inconsistent error handling between two methods on the same class, and unused, commented-out code.

### 7.7 Infrastructure and Deployment (`charts/`, `configuration/`, `.github/`)

**Secrets and credentials**

- The RabbitMQ default credentials and Erlang cookie finding is described in [Top Risks](#72-top-risks) above. Severity: Critical.
- One demo script contains a fixed OAuth client secret, written directly into the script. Severity: High.
- One example workflow file contains object-storage credentials, encoded in base64 but not encrypted, committed directly to the repository. Severity: High.
- A metrics-scraping configuration file, and a checked-in copy of a live cluster's config map, both contain the same plain-text broker credentials. Severity: Medium.

**Access control**

- The missing NetworkPolicy finding is described in [Top Risks](#72-top-risks) above. Severity: Critical.
- The Grafana deployment allows anonymous access at the administrator role, with its login form turned off. Severity: High.
- The service mesh's own authorization policy for the observability tools allows any caller with no authentication check. This removes the protection the service mesh is meant to add. Severity: Medium.
- Several internal scraping connections disable TLS certificate verification. Severity: Low.

**Baseline Kubernetes configuration**

- No chart defines a liveness probe or a readiness probe on any deployment. Severity: High.
- Every chart defines resource requests and limits, but every one of these definitions is commented out and inactive. Severity: High.
- Every service chart sets a fixed replica count of one, with no autoscaler and no disruption budget defined anywhere. Each core service is a single point of failure. Severity: High.
- The message broker runs as a single, unclustered deployment, with no backup process defined. Severity: High.
- The etcd cluster's starting member list is fixed, with no documented process to grow the cluster, replace a failed member, or back up its data. This etcd cluster holds the platform's live policy and agreement data. Severity: High.
- No container in any chart sets a security context or a `runAsNonRoot` setting. One chart contains a commented-out template for this setting, never turned on. Severity: Medium.
- No persistent volume claim in the repository sets an explicit storage class. Each claim depends on whatever storage class the cluster provides by default. Severity: Medium.

**Container images and supply chain**

- Every service deploys using a moving branch tag, not a fixed version. This removes any rollback guarantee. Severity: High.
- Some third-party container images stay pinned to the `latest` tag, even in charts that pin a fixed version for every other image. Severity: Medium.
- The dependency-update configuration covers only the devcontainer files. It does not cover the Go modules or the Docker base images. Severity: Medium.
- No CI workflow runs a static-analysis security scan or a container-image scan. Severity: Medium.

**Access permissions (RBAC)**

- One cluster-wide role grants broad create and delete permissions on core resources across every namespace. No current workload uses this role. Severity: Medium.
- One role that creates Jobs references an old, removed API group name. Severity: Low.
- One chart's access-control setup depends on a role defined in a separate chart. Installing the charts in the wrong order leaves this reference broken. Severity: Low.

**Demo and test configuration near the production deployment path**

- One test-only chart duplicates the names of resources from the production `core` chart, in the same namespace. Installing both charts together would cause a naming conflict. Severity: Medium.
- One demo script creates resources directly inside a production service's namespace, outside of any Helm chart, and changes a security-related environment variable on a running deployment through a direct `kubectl set env` command. Severity: Medium.
- The cluster's startup process loads its initial policy and catalog data through an unauthenticated download from a moving branch on GitHub, with no integrity check on the downloaded content. Severity: Medium.

**Assumptions specific to the local development cluster**

- The Grafana dashboard configuration depends on a host-path volume mount to the repository's own location on disk. This setup works only on the documented single-node local cluster, and fails on any real multi-node cluster. Severity: High.
- The log-collection setup assumes one specific container-runtime log file layout. This layout does not match every managed cloud Kubernetes provider. Severity: Medium.
- Every externally reachable service uses a NodePort. This pattern does not map cleanly onto a managed cloud cluster. Severity: Medium.

**General housekeeping**

- The repository contains several stale or unused files inside the deployment path: leftover Helm scaffold files, old manifests from a service mesh setup DYNAMOS no longer uses, one duplicate committed file, one prebuilt archive committed alongside its own source files, and two test cases permanently skipped in the CI configuration instead of fixed. Each of these is a low-severity finding on its own, but together they are worth a cleanup pass. Severity: Low.

---

This review was produced using a LLM, working from the codebase, the git history, and internal design notes. Section 7 in particular reflects an automated code review. A developer must treat Section 7 as a starting point for review, not as a finished audit._

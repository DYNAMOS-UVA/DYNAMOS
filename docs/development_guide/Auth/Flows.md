# Authentication flows

## 1. Data request from an end user

```mermaid
sequenceDiagram
    participant U as User
    participant G as api-gateway
    participant P as policy-enforcer
    participant O as orchestrator
    participant A as agent (UVA)
    U->>G: POST /api/v1/requestApproval<br/>Authorization: Bearer <user token>
    G->>G: verify token (AUTH_*), user = token subject
    G->>P: requestApproval (RabbitMQ, user_id=api-gateway)
    P->>O: validationResponse (RabbitMQ, user_id=policy-enforcer)
    O->>O: mint one job token per agent (TOKEN_ISSUER_*)
    O->>A: compositionRequest (RabbitMQ, user_id=orchestrator)
    O->>G: requestApprovalResponse + provider_tokens (RabbitMQ)
    G->>A: POST /agent/v1/sqlDataRequest/uva<br/>Authorization: Bearer <job token for UVA>
    A->>A: verify token, check job and user
    A-->>G: result
    G-->>U: result
```

Steps:

1. The user sends the request with a bearer token. api-gateway verifies the token with its `AUTH_*` providers (for example `oidc`).
2. api-gateway takes the user from the token subject. It ignores the user name in the request body. The token subject must be the user name in the etcd agreements, because policy-enforcer matches on it.
3. policy-enforcer checks the agreements. The orchestrator makes the job.
4. The orchestrator mints one job token for each agent of the job (`go/cmd/orchestrator/job_tokens.go`):

   | Claim | Value |
   |-------|-------|
   | `iss` | `dynamos-orchestrator` |
   | `aud` | the agent (for example `UVA`) |
   | `sub` | the user |
   | `scope` | `agent:<requestType>` (for example `agent:sqlDataRequest`) |
   | `attrs.jobId` | the job ID |
   | `exp` | 5 minutes after issue |

   The tokens go to api-gateway in the field `provider_tokens` of `RequestApprovalResponse`.
5. api-gateway sends each agent its own token.
6. The agent verifies the token (signature, issuer, audience = its `DATA_STEWARD_NAME`, scope). Then it checks that the token subject is the user of the request and that `attrs.jobId` is the job of the request (`authorizeJob` in `go/cmd/agent/token_validation.go`). A token for one job or one agent does not work for another.

## 2. Data request from a DSP participant

```mermaid
sequenceDiagram
    participant C as DSP consumer
    participant D as dsp-connector
    participant T as transfer-process-service
    participant G as api-gateway
    C->>D: Transfer Request<br/>Authorization: <DAT>
    D->>D: verify DAT (DID signature), participant = DID
    D->>D: agreement is FINALIZED and owned by the participant
    D->>T: create transfer (internal API)
    T->>T: mint participant token (TOKEN_ISSUER_*)
    T->>G: POST /api/v1/requestApproval<br/>Authorization: Bearer <participant token>
    Note over G: then the same steps as flow 1
```

transfer-process-service mints this token (`go/cmd/transfer-process-service/gateway_auth.go`):

| Claim | Value |
|-------|-------|
| `iss` | `dynamos-transfer-process-service` |
| `aud` | `api-gateway` (`GATEWAY_TOKEN_AUDIENCE` changes it) |
| `sub` | the participant DID |
| `kind` | `participant` |
| `scope` | `gateway:requestApproval` |
| `exp` | 2 minutes after issue |

api-gateway accepts a token of kind `service` or `participant` only when it has the scope `gateway:requestApproval`.

> The internal API between dsp-connector and transfer-process-service does not authenticate its caller yet. See the known limits in the [README](./README.md#known-limits).

## 3. Orchestrator API (operators and tools)

The orchestrator API (`/api/v1/...`) needs a token from the `AUTH_*` providers of the orchestrator:

| Request | Scope |
|---------|-------|
| `GET`, `HEAD` | `orchestrator:read` (or `orchestrator:write`) |
| `PUT`, `POST` and other methods | `orchestrator:write` |
| `/updateEtc` (any method) | `orchestrator:write` |

`ORCHESTRATOR_READ_SCOPE` and `ORCHESTRATOR_WRITE_SCOPE` change the scope names, for example to Entra app role values. The orchestrator answers 401 without a valid token and 403 without the scope.

## 4. RabbitMQ messages

Each sidecar connects with its own RabbitMQ user and sets the AMQP `user_id` of each message to that user. RabbitMQ refuses a message with a different `user_id`. The receiving sidecar drops a message when its sender may not send that type (`go/cmd/sidecar/sender_auth.go`):

| Message type | Allowed senders |
|--------------|-----------------|
| `requestApproval` | `api-gateway` |
| `validationResponse` | `policy-enforcer` |
| `policyUpdate` | `policy-enforcer`, `orchestrator` |
| `compositionRequest` | `orchestrator` |
| `requestApprovalResponse` | `orchestrator` |
| `sqlDataRequest` | `agent-*` |
| `microserviceCommunication` | `agent-*`, `job-*` |

A message without `user_id` is dropped. The broker permissions also limit which queues each user can read and which routing keys it can publish to. See [Operations](./Operations.md#rabbitmq-users).

## 5. Browser access (CORS)

orchestrator, api-gateway and the agents send CORS headers only for the origins in `CORS_ALLOWED_ORIGINS`. When the variable is empty, they send no CORS headers, and browsers block calls from other origins. Server-to-server callers do not use CORS.

# Auth operations

## Keys, passwords and Secrets

`scripts/auth-secrets.sh` makes all auth material and puts it in Kubernetes Secrets. `./dynamos-dev.sh up` and `configuration/dynamos-configuration.sh` run it. You can run it again at any time: it adds only what is missing and does not replace a key or a password.

The files are in `.dev/auth` (git ignores this directory). `AUTH_KEYS_DIR` changes the directory.

| File | Use |
|------|-----|
| `orchestrator.key.pem`, `orchestrator.pub.pem` | The orchestrator signs job tokens. The agents verify them. |
| `transfer-process-service.key.pem`, `.pub.pem` | transfer-process-service signs participant tokens. api-gateway verifies them. |
| `dsp-connector.key.pem`, `.pub.pem` | dsp-connector signs service tokens for the internal API of transfer-process-service. transfer-process-service verifies them. |
| `dev-user-token`, `dev-operator-token` | Tokens for the `static-dev` provider (Tilt only). |
| `rabbitmq/<user>.password` | One RabbitMQ password for each user. |

The Secret `dynamos-auth` (mounted at `/etc/dynamos/auth`):

| Namespace | Keys |
|-----------|------|
| `orchestrator` | `orchestrator.key.pem`, `dev-operator-token` |
| `uva`, `vu`, `surf` | `orchestrator.pub.pem` |
| `transfer-process-service` | `transfer-process-service.key.pem`, `dsp-connector.pub.pem` |
| `api-gateway` | `transfer-process-service.pub.pem`, `dev-user-token` |
| `dsp-connector` | `dsp-connector.key.pem` |

The Secret `dynamos-rabbitmq` (read by the sidecars as `AMQ_PASSWORD`):

| Namespace | Keys |
|-----------|------|
| `orchestrator` | `orchestrator`, `policy-enforcer` |
| `api-gateway` | `api-gateway` |
| `uva`, `vu`, `surf` | `agent-<s>`, `job-<s>` |

Commands:

| Command | What it does |
|---------|--------------|
| `scripts/auth-secrets.sh` | Makes missing keys and passwords, applies the Secrets and the RabbitMQ users. |
| `scripts/auth-secrets.sh status` | Shows each key, password and open rotation. |
| `scripts/auth-secrets.sh --print` | Shows the dev tokens and the RabbitMQ admin password as shell exports. `./dynamos-dev.sh tokens` does the same. |
| `scripts/auth-secrets.sh rotate NAME` | Replaces a key (see below). |
| `scripts/auth-secrets.sh retire NAME` | Removes the old key after a rotation. |

## Dev tokens

Only the Tilt images accept the dev tokens (`-tags authdev`). The production images do not start with the `static-dev` provider.

| Token | Service | Identity |
|-------|---------|----------|
| `DYNAMOS_USER_TOKEN` | api-gateway | user `jorrit.stutterheim@cloudnation.nl` |
| `DYNAMOS_OPERATOR_TOKEN` | orchestrator | service `dynamos-operator`, scopes `orchestrator:read`, `orchestrator:write` |

`scripts/test_system_behavior.py` reads both variables. `go/cmd/loadtest` reads the orchestrator token from `DYNAMOS_TOKEN`.

## Mint a token by hand

`go/cmd/authtool` makes keys and tokens for the `jwt` provider. Use it for debugging and operations only.

```bash
cd go
# Key pair (same format as: openssl genpkey -algorithm ed25519)
go run ./cmd/authtool keygen -name my-service -out ./keys

# Job token for agent UVA
go run ./cmd/authtool token -key ../.dev/auth/orchestrator.key.pem \
  -issuer dynamos-orchestrator -sub jorrit.stutterheim@cloudnation.nl \
  -aud UVA -scope agent:sqlDataRequest -attr jobId=<job id> -ttl 5m
```

## Key rotation

The verifiers accept a token from any key in their public key file. A rotation uses this overlap, so tokens stay valid during the change:

1. Run `scripts/auth-secrets.sh rotate orchestrator`. The script:
   1. makes the next key;
   2. lets the verifiers trust the current and the next key, then restarts the verifiers and waits;
   3. lets the issuer sign with the next key, then restarts the issuer and waits.
2. Wait for the grace period (default 600 seconds, `AUTH_ROTATION_GRACE`). Tokens from the old key expire in this time.
3. Run `scripts/auth-secrets.sh retire orchestrator`. The verifiers stop trusting the old key. The command refuses to run before the grace period ends. `--force` skips the wait.

If `rotate` stops before it is complete, run it again. It continues from the step where it stopped.

| Key | Verifiers | Issuer |
|-----|-----------|--------|
| `orchestrator` | `uva`, `vu`, `surf` | `orchestrator` |
| `transfer-process-service` | `api-gateway` | the three transfer-process-service deployments |
| `dsp-connector` | the three transfer-process-service deployments | the three dsp-connector deployments |

Dev tokens and RabbitMQ passwords have no overlap:

- `rotate dev-user-token` (or `dev-operator-token`) replaces the token and restarts the service that accepts it.
- `rotate rabbitmq` replaces all RabbitMQ passwords and restarts all services with a sidecar. Open connections stay up. Running jobs keep their connection; new jobs use the new password.

## Run the DSP services without Kubernetes

transfer-process-service does not start without `AUTH_PROVIDERS`, and dsp-connector does not start without `TOKEN_ISSUER_PROVIDER`. This is also true for `go run -tags local`. To run them on your computer (for example for the DSP TCK), make a key pair and give each service its half:

```bash
cd go
go run ./cmd/authtool keygen -name dsp-connector -out ../.dev/auth-local

AUTH_PROVIDERS=jwt \
AUTH_JWT_ISSUER=dynamos-dsp-connector \
AUTH_JWT_AUDIENCE=transfer-process-service \
AUTH_JWT_PUBLIC_KEY_FILES=../.dev/auth-local/dsp-connector.pub.pem \
DATA_STEWARD_NAME=VU go run -tags local ./cmd/transfer-process-service

TOKEN_ISSUER_PROVIDER=jwt \
TOKEN_ISSUER_JWT_ISSUER=dynamos-dsp-connector \
TOKEN_ISSUER_JWT_PRIVATE_KEY_FILE=../.dev/auth-local/dsp-connector.key.pem \
DATA_STEWARD_NAME=VU go run -tags local ./cmd/dsp-connector
```

The DSP TCK workflow (`.github/workflows/dsp-tck.yml`) does the same with a key pair that it makes for each run.

## RabbitMQ users

Each sidecar has its own RabbitMQ user (`scripts/auth-rabbitmq.sh`):

| User | Sidecar of | Can read | Can publish to |
|------|------------|----------|----------------|
| `orchestrator` | orchestrator | `orchestrator-in` | `policyEnforcer-in`, `api-gateway-in`, `<STEWARD>-in` |
| `policy-enforcer` | policy-enforcer | `policyEnforcer-in` | `orchestrator-in` |
| `api-gateway` | api-gateway | `api-gateway-in` | `policyEnforcer-in` |
| `agent-<s>` | agent `<S>` | `<S>-in`, its job queues | `<STEWARD>-in` (third party), its job queues |
| `job-<s>` | jobs of agent `<S>` | its job queues | `<S>-in` |
| `dynamos-admin` | (management UI) | all | all |

`<s>` is the steward in lower case, `<S>` in upper case. A job queue name ends with the steward and a counter (for example `jorrit-stutterheim-1a2b3c4duva1`). The permissions depend on this name format.

The script also:

- removes the old users `normal_user` and `guest`;
- writes the broker state to the definitions file on the RabbitMQ volume (`/mnt/definitions.json`), so a restart does not add the old users again.

To open the management UI, run `./dynamos-dev.sh tokens`, then log in as `dynamos-admin` with `DYNAMOS_RABBITMQ_ADMIN_PASSWORD`.

## Troubleshooting

| Problem | Cause and solution |
|---------|--------------------|
| A service stops at startup with `Failed to configure ... authentication` | The auth settings are missing or wrong. Read the error. For the orchestrator in production, set `AUTH_PROVIDERS`. |
| `staticdev: provider is disabled in this build` | The image was built without `-tags authdev`. Use the Tilt image, or remove `static-dev` from the configuration. |
| A pod stays in `ContainerCreating` with `secret "dynamos-auth" not found` | Run `scripts/auth-secrets.sh` (or `./dynamos-dev.sh up`). |
| `401` from api-gateway or the orchestrator | The token is missing, expired, from another issuer, or for another audience. The service log has the reason (never the token). |
| dsp-connector: `transfer-process-service returned 401` | transfer-process-service does not trust the key of dsp-connector. Run `scripts/auth-secrets.sh`, then restart the transfer-process-service pods. |
| `403` from transfer-process-service | The token is not a service token, or it does not have the scope `transfer-process-service:internal`. Only dsp-connector can call the internal API. |
| `403` from the orchestrator | The token has no `orchestrator:write` scope (or no `orchestrator:read` scope for a read). |
| `403` from an agent | The job token is for another job or user. |
| Sidecar: `Exception (403) Reason: "username or password not allowed"` | The pod has an old RabbitMQ password or user. Run `scripts/auth-secrets.sh`, then restart the pod. |
| Sidecar: `Dropped RabbitMQ message` | The sender may not send this message type, or the message has no `user_id`. Check that all sidecars run the same image. |
| Sidecar: `Message to <queue> failed: broker refused the message` | The user may not publish to that routing key. Check the queue name against the table above. |
| Job pods do not get messages | The job queue name does not end with the steward and a counter, so the `job-<s>` user can not read it. |

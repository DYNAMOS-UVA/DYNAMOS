# Authentication

This folder describes how DYNAMOS services authenticate their callers and each other.

## Table of Contents

- [Design](./Design.md): why authentication is a library with providers, not an auth service, and what this means.
- [Flows](./Flows.md): which token goes where, step by step.
- [Configuration](./Configuration.md): the environment variables and Helm values of each service.
- [Operations](./Operations.md): keys, Secrets, dev tokens, key rotation, RabbitMQ users and troubleshooting.
- [Adding a provider](./Adding-a-provider.md): how to add a new auth method (for example for NLDT).

## Summary

- The code is in `go/pkg/auth`. It defines contracts only: `Verifier`, `Issuer`, `Principal`, `Middleware`, `RequireScope` and a provider `Registry`.
- A **provider** is one auth method. It is a separate package that registers itself by name. The providers are:

  | Name | Package | Use |
  |------|---------|-----|
  | `jwt` | `go/pkg/auth/jwtauth` | DYNAMOS service tokens (Ed25519). Verifier and issuer. |
  | `oidc` | `go/pkg/auth/oidc` | End-user tokens from an OpenID Connect identity provider (for example Microsoft Entra ID or Keycloak). |
  | `static-dev` | `go/pkg/auth/staticdev` | One fixed token for local development. Works only in images built with `-tags authdev` (Tilt). |
  | `dat` | `go/cmd/dsp-connector` | DSP participant tokens (DAT) signed by a DID. |

- Each service selects its providers from environment variables. A change of auth method is a configuration change, not a code change.
- The handlers read the identity from the verified `Principal`, never from the request body.
- RabbitMQ messages carry their sender. Each sidecar has its own RabbitMQ user, and the receiver checks which user may send which message type.

## Quick start (Tilt)

1. Run `./dynamos-dev.sh up`. It makes the keys, the dev tokens, the RabbitMQ users and the Kubernetes Secrets.
2. Run `tilt up`.
3. Load the dev tokens into your shell:

   ```bash
   eval "$(./dynamos-dev.sh tokens)"
   ```

4. Send a request with the user token:

   ```bash
   curl http://localhost:8080/api/v1/requestApproval \
     -H 'Host: api-gateway.api-gateway.svc.cluster.local' \
     -H "Authorization: Bearer $DYNAMOS_USER_TOKEN" \
     -H 'Content-Type: application/json' \
     --data-raw '{...}'
   ```

   Use `$DYNAMOS_OPERATOR_TOKEN` for the orchestrator API and for the internal API of negotiation-service.

## Known limits

- The internal API of catalog-service (`/internal/v1/...`) does not authenticate its callers yet. A separate issue tracks this work. Do not expose this service outside the cluster. The internal APIs of transfer-process-service and negotiation-service need a token (see [Flows](./Flows.md#2-data-request-from-a-dsp-participant)).
- Bearer tokens are not bound to the sender. A job token stays valid until it expires (5 minutes).
- There is no token revocation. Use short lifetimes and key rotation.
- The services read their keys at startup. A key rotation restarts pods.
- The keys and passwords are files on one computer (`.dev/auth`). For production, use a secret manager that writes the same Secret layout.
- One environment prefix can use each provider name only once. For example, a service can not configure two `oidc` providers through environment variables. Build them in code with `Registry.BuildAll` if you need this.
- The steward list (`uva`, `vu`, `surf`) is fixed in `scripts/auth-secrets.sh`, `scripts/auth-rabbitmq.sh` and the charts.

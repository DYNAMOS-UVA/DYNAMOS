# Auth design

## Context

Before this change, authentication outside the DSP services did not work:

- policy-enforcer put the fixed token `"1234"` in every job approval.
- api-gateway sent `"bearer 1234"` to the agents.
- The agent middleware did not check the token.
- The orchestrator HTTP API had no authentication. Any caller could replace agreements, archetypes and request types.
- api-gateway used the user name from the request body.
- All services used one RabbitMQ user, `normal_user`, with all rights. The RabbitMQ `guest` user was an administrator with a known password, and it worked from any pod.

DYNAMOS must support more than one auth method:

- DSP participants, identified by a DID and a DAT.
- End users, through an OpenID Connect identity provider.
- DYNAMOS services that call each other.
- Future dataspaces with their own trust framework (for example NLDT).

## Decision

1. **A library, not a service.** `go/pkg/auth` is a Go package that each service imports. It verifies tokens locally. There is no central auth service in the request path.
2. **Contracts and providers.** `go/pkg/auth` defines the contracts (`Verifier`, `Issuer`, `Principal`). Each auth method is a provider package that registers a factory by name (like `database/sql` drivers). A service never imports the internals of a provider.
3. **Configuration selects providers.** A service reads `<PREFIX>_PROVIDERS` (verifiers) or `<PREFIX>_PROVIDER` (issuer) and the settings `<PREFIX>_<NAME>_<SETTING>` from its environment. Helm values set these variables.
4. **Identity comes from the token.** Handlers read the caller from `auth.PrincipalFrom(ctx)`. They do not read the user from the request body.
5. **Short-lived, bound tokens between services.** The orchestrator mints one token for each agent of a job. The token is bound to the agent (`aud`), the user (`sub`), the job (`attrs.jobId`) and the request type (`scope`). It is valid for 5 minutes. transfer-process-service mints a token for api-gateway when it starts a job for a DSP participant.
6. **Ed25519 service keys.** The `jwt` provider signs with Ed25519 (`EdDSA`). The key ID (`kid`) is the RFC 7638 thumbprint of the public key, so no key ID needs configuration. A verifier can trust several public keys, which allows key rotation without downtime.
7. **Fail closed.** A service with a missing or broken auth configuration does not start. The one exception is transfer-process-service: without an issuer it starts, but it refuses to start jobs. This keeps the DSP TCK run working.
8. **Dev-only provider behind a build tag.** `static-dev` accepts one fixed token. Its factory refuses to build unless the binary was built with `-tags authdev`. Only `go/Dockerfile.tilt` sets this tag.
9. **Authenticated RabbitMQ senders.** Each sidecar has its own RabbitMQ user with limited rights. It puts its user in the AMQP `user_id` property. RabbitMQ refuses a wrong `user_id`, and the receiving sidecar checks which user may send which message type.

## Alternatives that we did not select

- **A separate auth microservice** (token exchange in the request path). It adds one more service to deploy and to keep available, and a network call on each request. The library gives the same modularity without these costs. A token-exchange service can still be a provider later.
- **Use one provider for everything** (for example only OIDC). DSP participants and DYNAMOS services do not have accounts at an identity provider. A single method does not fit all callers.
- **Keep the user name in the request body and check it.** The body is not authenticated. Only a verified token can tell who the caller is.
- **Sign each RabbitMQ message.** This needs keys in every sidecar and job pod. RabbitMQ already authenticates the connection and can validate `user_id`, so the broker gives the same result with less code.

## Consequences

- A new auth method is a new provider package plus configuration. The services do not change (see [Adding a provider](./Adding-a-provider.md)).
- Each deployment must give every service its keys and settings. `scripts/auth-secrets.sh` does this for the dev cluster and for `dynamos-configuration.sh`.
- The orchestrator does not start in production until an identity provider is configured for its API (`AUTH_PROVIDERS`).
- The services read keys at startup. A key rotation restarts pods (see [Operations](./Operations.md)).
- The RabbitMQ permissions depend on the queue names (`<STEWARD>-in`, and job queues that end with the steward name and a counter). A change of the queue names must also change `scripts/auth-rabbitmq.sh`.

# Auth configuration

## Environment variable format

A service reads its auth configuration from environment variables under one prefix (`go/pkg/auth/env.go`):

| Variable | Meaning |
|----------|---------|
| `<PREFIX>_PROVIDERS=oidc,jwt` | The verifiers, in the order that the service tries them. |
| `<PREFIX>_PROVIDER=jwt` | One issuer (for services that mint tokens). |
| `<PREFIX>_<NAME>_<SETTING>=value` | One setting of provider `<NAME>`. |

`<NAME>` is the provider name in upper case, with `-` written as `_` (`static-dev` becomes `STATIC_DEV`). `<SETTING>` becomes the lower-case settings key (`AUTH_JWT_PUBLIC_KEY_FILES` becomes `public_key_files`).

The service tries the verifiers in order. A verifier that does not recognize a token (for example a JWT from another issuer) passes the token to the next verifier. The service does not start when a provider is unknown or its settings are wrong.

## Providers and their settings

### `jwt` (DYNAMOS service tokens)

Verifier settings:

| Setting | Required | Default | Meaning |
|---------|----------|---------|---------|
| `issuer` | yes | | Expected `iss`. |
| `audience` | yes | | Comma-separated. The token must name one. |
| `public_key_files` | one of the two | | Comma-separated PEM files. A file can hold more than one key (key rotation). |
| `public_keys_pem` | one of the two | | The PEM blocks themselves. |
| `leeway` | no | `30s` | Allowed clock difference. |

Issuer settings:

| Setting | Required | Default | Meaning |
|---------|----------|---------|---------|
| `issuer` | yes | | `iss` of the minted tokens. |
| `private_key_file` | one of the two | | PKCS #8 Ed25519 PEM file. |
| `private_key_pem` | one of the two | | The PEM itself. |
| `default_ttl` | no | `5m` | Lifetime when the caller gives none. |
| `max_ttl` | no | `1h` | Maximum lifetime. |

### `oidc` (end users)

| Setting | Required | Default | Meaning |
|---------|----------|---------|---------|
| `issuer` | yes | | Exact expected `iss` (https URL). Entra v2: `https://login.microsoftonline.com/<tenant>/v2.0`. |
| `audience` | yes | | Comma-separated. Entra: the client ID or App ID URI of the API. |
| `jwks_url` | no | from discovery | Skips OIDC discovery (https URL). |
| `subject_claim` | no | `sub` | Claim used as the user. It must match the user names in the etcd agreements. |
| `scope_claims` | no | `scp,scope,roles` | Claims read as scopes. |
| `attribute_claims` | no | | Claims copied to the principal attributes (for example `email,name`). |
| `algorithms` | no | `RS256,ES256` | Allowed signature algorithms. |
| `leeway` | no | `60s` | Allowed clock difference. |
| `cache_ttl` | no | `15m` | JWKS cache lifetime. |
| `min_refresh` | no | `30s` | Minimum time between JWKS fetches. |
| `http_timeout` | no | `10s` | Timeout for discovery and JWKS calls. |

When the identity provider is not available, the service uses the cached keys. Without cached keys it answers 503.

### `static-dev` (local development only)

| Setting | Required | Default | Meaning |
|---------|----------|---------|---------|
| `token` | yes | | The shared token. |
| `subject` | yes | | The user or service name of the token. |
| `kind` | no | `service` | `user`, `service` or `participant`. |
| `scopes` | no | | Comma-separated scopes. |
| `issuer` | no | `static-dev` | Issuer of the principal. |

The provider works only in a binary built with `-tags authdev`. In other builds the service does not start with this provider.

## Settings per service

| Service | Prefix | Purpose | Default in the chart |
|---------|--------|---------|----------------------|
| orchestrator | `AUTH` | Callers of the orchestrator API | Empty. **You must set it** (for example `oidc`). Tilt adds `static-dev`. |
| orchestrator | `TOKEN_ISSUER` | Job tokens for the agents | `jwt`, key `/etc/dynamos/auth/orchestrator.key.pem` |
| api-gateway | `AUTH` | Callers of the public API | `jwt` for transfer-process-service tokens. Add `oidc` for end users. Tilt adds `static-dev`. |
| agent | `AUTH` | Job tokens from the orchestrator | `jwt`, key `/etc/dynamos/auth/orchestrator.pub.pem`. The audience defaults to `DATA_STEWARD_NAME`. |
| transfer-process-service | `TOKEN_ISSUER` | Participant tokens for api-gateway | `jwt`, key `/etc/dynamos/auth/transfer-process-service.key.pem` |

Other variables:

| Variable | Service | Meaning |
|----------|---------|---------|
| `ORCHESTRATOR_READ_SCOPE` | orchestrator | Scope for reads. Default `orchestrator:read`. |
| `ORCHESTRATOR_WRITE_SCOPE` | orchestrator | Scope for writes. Default `orchestrator:write`. |
| `GATEWAY_TOKEN_AUDIENCE` | transfer-process-service | `aud` of its tokens. Default `api-gateway`. |
| `CORS_ALLOWED_ORIGINS` | orchestrator, api-gateway, agent | Comma-separated browser origins (`scheme://host[:port]`). Empty: no CORS headers. |
| `AMQ_USER`, `AMQ_PASSWORD` | sidecar | The RabbitMQ user of the sidecar. |
| `AMQ_VERIFY_SENDER` | sidecar | `false` turns the sender check off. Use it only with a local broker where all services share one user. |

## Helm values

The charts `orchestrator`, `api-gateway`, `agents`, `thirdparty` and `transfer-process-service` have the same auth values (`templates/_auth.tpl`):

```yaml
auth:
  secretName: dynamos-auth     # mounted read-only at /etc/dynamos/auth
  env:                         # pkg/auth variables; empty values are not set
    AUTH_PROVIDERS: oidc,jwt
    AUTH_OIDC_ISSUER: https://login.microsoftonline.com/<tenant>/v2.0
    AUTH_OIDC_AUDIENCE: api://dynamos
  devToken:                    # orchestrator and api-gateway only
    enabled: false             # Tilt sets true
    secretKey: dev-user-token
    subject: jorrit.stutterheim@cloudnation.nl
    kind: user
    scopes: ""
corsAllowedOrigins: ""
```

When `devToken.enabled` is `true`, the chart adds `static-dev` to `AUTH_PROVIDERS` and reads the token from the Secret. The `_auth.tpl` file is the same in all five charts. Change all copies together.

## Production example

Orchestrator API with Entra ID app roles:

```yaml
auth:
  env:
    AUTH_PROVIDERS: oidc
    AUTH_OIDC_ISSUER: https://login.microsoftonline.com/<tenant>/v2.0
    AUTH_OIDC_AUDIENCE: api://dynamos-orchestrator
    AUTH_OIDC_SUBJECT_CLAIM: oid
    ORCHESTRATOR_READ_SCOPE: Orchestrator.Read
    ORCHESTRATOR_WRITE_SCOPE: Orchestrator.Write
```

api-gateway with end users and transfer-process-service:

```yaml
auth:
  env:
    AUTH_PROVIDERS: oidc,jwt
    AUTH_OIDC_ISSUER: https://login.microsoftonline.com/<tenant>/v2.0
    AUTH_OIDC_AUDIENCE: api://dynamos
    AUTH_OIDC_SUBJECT_CLAIM: preferred_username
```

The `AUTH_JWT_*` values from the chart stay in place for the transfer-process-service tokens.

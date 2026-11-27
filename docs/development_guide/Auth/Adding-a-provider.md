# Adding a provider

A provider is one auth method, for example the trust framework of a new dataspace (NLDT). You add a provider as a new package. The services and `go/pkg/auth` do not change.

## 1. Write the verifier

Make a package, for example `go/pkg/auth/nldt`. It must:

- implement `auth.Verifier`: `Verify(ctx, token) (auth.Principal, error)`;
- register a factory by name in `init`;
- read its settings from `auth.Settings`.

```go
// Package nldt is an EXAMPLE skeleton. It shows the structure only.
package nldt

import (
	"context"
	"fmt"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
)

// Name is the registry name. Environment prefix: <PREFIX>_NLDT_<SETTING>.
const Name = "nldt"

func init() {
	auth.Register(Name, Factory)
}

// Factory builds the verifier. Return auth.ErrProviderConfig for bad
// settings: the service then does not start.
func Factory(s auth.Settings) (auth.Verifier, error) {
	trustList, err := s.Require("trust_list_url")
	if err != nil {
		return nil, err
	}
	return &verifier{trustList: trustList}, nil
}

type verifier struct{ trustList string }

func (v *verifier) Verify(ctx context.Context, token string) (auth.Principal, error) {
	if !looksLikeNLDTToken(token) {
		// Not our format: auth.Any tries the next provider.
		return auth.Principal{}, fmt.Errorf("%w: not an NLDT token", auth.ErrUnsupportedToken)
	}
	// Check the signature, issuer, audience and expiry here.
	// Wrong token:            wrap auth.ErrInvalidCredentials.
	// Trust list unavailable: wrap auth.ErrUnavailable (the service answers 503).
	return auth.Principal{
		Subject:  "did:example:party", // the identity the agreements use
		Issuer:   "https://trust.example",
		Kind:     auth.KindParticipant,
		Provider: Name,
	}, nil
}
```

Rules:

- **Never** put the token in an error message or a log line.
- Use the sentinel errors of `go/pkg/auth` (`ErrUnsupportedToken`, `ErrInvalidCredentials`, `ErrUnavailable`, `ErrForbidden`, `ErrProviderConfig`). The middleware maps them to 401, 503 and 403. Any other error becomes 401.
- Return `ErrUnsupportedToken` only when the token is not in your format. Then the service can try its other providers. When the token is in your format but is wrong, return `ErrInvalidCredentials`.
- `Subject` must be the identity that the etcd agreements use (`Relations` keys), because policy-enforcer matches on it.
- Put extra claims in `Attributes`, not in new fields.

## 2. Add an issuer (only if needed)

A provider that also mints tokens implements `auth.Issuer` and registers it with `auth.RegisterIssuer(Name, IssuerFactory)`. See `go/pkg/auth/jwtauth/issuer.go`. An issuer must refuse a token without a subject or an audience.

## 3. Write the tests

Test at least:

- a valid token gives the expected `Principal`;
- a token from another format gives `ErrUnsupportedToken`;
- an expired token, a wrong signature, a wrong issuer and a wrong audience give `ErrInvalidCredentials`;
- a missing setting gives `ErrProviderConfig`;
- an outage of the remote side gives `ErrUnavailable`.

`go/pkg/auth/oidc/oidc_test.go` and `go/pkg/auth/jwtauth/jwtauth_test.go` are good examples.

## 4. Enable it in a service

1. Add a blank import in the auth file of the service, next to the other providers (for example `go/cmd/api-gateway/auth.go`):

   ```go
   _ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/nldt"
   ```

2. Add the provider to the Helm values of the service:

   ```yaml
   auth:
     env:
       AUTH_PROVIDERS: oidc,jwt,nldt
       AUTH_NLDT_TRUST_LIST_URL: https://trust.example/list
   ```

3. If the provider needs files (keys, certificates), add them to the `dynamos-auth` Secret in `scripts/auth-secrets.sh`. The chart mounts the Secret at `/etc/dynamos/auth`.

The service tries the providers in the order of `AUTH_PROVIDERS`. Put the provider that rejects foreign tokens fastest (without a network call) first.

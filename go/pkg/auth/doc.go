// Package auth is the shared authentication core for DYNAMOS services.
//
// The package defines contracts only. It does not know how a token is
// signed, which identity provider issued it, or which protocol (DSP,
// OIDC, internal JWT, NLDT, ...) produced it. Each such method is a
// provider: a separate package that implements Verifier (and optionally
// Issuer) and registers a Factory under a name in a Registry. A service
// selects providers by name from configuration and never imports a
// provider's internals.
//
// Request flow inside a service:
//
//	HTTP request
//	  -> Middleware: read the bearer token (BearerToken)
//	  -> Verifier.Verify: provider checks the token, returns a Principal
//	  -> WithPrincipal: Principal stored in the request context
//	  -> RequireScope (optional): route-level authorization
//	  -> handler: reads identity with PrincipalFrom, never from the body
//
// Rules the package enforces:
//   - Deny by default. A request without a verified Principal never
//     reaches the wrapped handler.
//   - Tokens are never logged or written to a response.
//   - Error responses carry a fixed message. Provider error details stay
//     server side (see WithErrorHandler to log them).
package auth

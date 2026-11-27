package main

import (
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/internalapi"

	// Providers the internal API can be configured with.
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/oidc"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/staticdev"
)

// internalAuthEnvPrefix selects the verifiers for callers of the internal
// API (/internal/v1/negotiations/...). There are two callers:
//
//   - dsp-connector, with its service tokens (jwt).
//   - Operators. On the Provider side, negotiation-service does not make
//     the offer, the agreement or the FINALIZED step by itself; an
//     operator sends them. FINALIZED also gives the participant access to
//     data (the policy-enforcer relation), so this is an admin action.
//     In production, add an identity provider (for example oidc) whose
//     tokens carry api.NegotiationServiceScope. Tilt adds static-dev.
//
// For example:
//
//	AUTH_PROVIDERS=jwt,oidc
//	AUTH_JWT_ISSUER=dynamos-dsp-connector
//	AUTH_JWT_AUDIENCE=negotiation-service
//	AUTH_JWT_PUBLIC_KEY_FILES=/etc/dynamos/auth/dsp-connector.pub.pem
const internalAuthEnvPrefix = "AUTH"

const internalAuthRealm = "dynamos-negotiation-service"

// mustBuildInternalVerifier loads the verifier. Without one the internal
// API would be open to every caller in the cluster, so a missing or broken
// configuration stops the process.
func mustBuildInternalVerifier() auth.Verifier {
	v, err := auth.VerifierFromEnv(internalAuthEnvPrefix)
	if err != nil {
		logger.Sugar().Fatalf("Failed to configure internal API authentication: %v", err)
	}
	return v
}

// newServer returns the HTTP handler of the service: /health without
// authentication, every other route only with a token that has
// api.NegotiationServiceScope. Service tokens (dsp-connector, operator
// tools) and user tokens (operators through an identity provider) are
// allowed. A participant token is refused, even with the scope: the
// internal API trusts its caller for the participant of a negotiation.
func newServer(v auth.Verifier) http.Handler {
	internal := http.NewServeMux()
	registerInternalRoutes(internal)
	return internalapi.Handler(internalapi.Config{
		Verifier: v,
		Scope:    api.NegotiationServiceScope,
		Kinds:    []auth.Kind{auth.KindService, auth.KindUser},
		Realm:    internalAuthRealm,
		OnReject: logInternalAuthFailure,
	}, healthHandler, internal)
}

func logInternalAuthFailure(r *http.Request, err error) {
	// err comes from pkg/auth and never contains the token.
	logger.Sugar().Infow("internal API request rejected", "method", r.Method, "path", r.URL.Path, "status", auth.StatusCode(err), "reason", err.Error())
}

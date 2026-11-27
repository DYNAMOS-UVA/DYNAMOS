package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"

	// Providers the gateway can be configured with. Each registers itself.
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/oidc"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/staticdev"
)

// authEnvPrefix selects the verifiers for callers of the public API.
// Typical production setup, end users through an IdP plus
// transfer-process-service through its own service tokens:
//
//	AUTH_PROVIDERS=oidc,jwt
//	AUTH_OIDC_ISSUER=https://login.microsoftonline.com/<tenant>/v2.0
//	AUTH_OIDC_AUDIENCE=api://dynamos
//	AUTH_OIDC_SUBJECT_CLAIM=preferred_username
//	AUTH_JWT_ISSUER=dynamos-transfer-process-service
//	AUTH_JWT_AUDIENCE=api-gateway
//	AUTH_JWT_PUBLIC_KEY_FILES=/etc/dynamos/auth/transfer-process-service.pub.pem
//
// The subject claim must hold the same user name the agreements in etcd
// use as Relations keys, because policy-enforcer matches on it.
const authEnvPrefix = "AUTH"

const authRealm = "dynamos-api-gateway"

func mustBuildCallerVerifier() auth.Verifier {
	v, err := auth.VerifierFromEnv(authEnvPrefix)
	if err != nil {
		logger.Sugar().Fatalf("Failed to configure caller authentication: %v", err)
	}
	return v
}

// callerAuth authenticates every request to the public API.
func callerAuth(v auth.Verifier) func(http.Handler) http.Handler {
	return auth.Middleware(v, auth.WithRealm(authRealm), auth.WithErrorHandler(logAuthFailure))
}

func logAuthFailure(w http.ResponseWriter, r *http.Request, err error) {
	// err comes from pkg/auth and never contains the token.
	logger.Sugar().Infow("request rejected", "path", r.URL.Path, "status", auth.StatusCode(err), "reason", err.Error())
	auth.DefaultErrorHandler(authRealm)(w, r, err)
}

// requestingUser returns the user a job request runs for: the verified
// subject, never the user name in the request body. End users act for
// themselves. Service and participant tokens (for example from
// transfer-process-service for a DSP participant) must also carry
// api.GatewayRequestScope, so a service token minted for some other
// purpose cannot start jobs.
func requestingUser(ctx context.Context) (string, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return "", fmt.Errorf("%w: request is not authenticated", auth.ErrMissingCredentials)
	}
	if p.Kind != auth.KindUser && !p.HasScope(api.GatewayRequestScope) {
		return "", fmt.Errorf("%w: %s token lacks scope %q", auth.ErrForbidden, p.Kind, api.GatewayRequestScope)
	}
	return p.Subject, nil
}

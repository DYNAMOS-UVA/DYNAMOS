package main

import (
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/internalapi"
	// jwtauth is registered by gateway_auth.go.
)

// internalAuthEnvPrefix selects the verifiers for callers of the internal
// API (/internal/v1/transfers/...). The only caller is dsp-connector, with
// its own service tokens:
//
//	AUTH_PROVIDERS=jwt
//	AUTH_JWT_ISSUER=dynamos-dsp-connector
//	AUTH_JWT_AUDIENCE=transfer-process-service
//	AUTH_JWT_PUBLIC_KEY_FILES=/etc/dynamos/auth/dsp-connector.pub.pem
//
// This is separate from TOKEN_ISSUER_*, which configures the tokens this
// service mints for api-gateway.
const internalAuthEnvPrefix = "AUTH"

const internalAuthRealm = "dynamos-transfer-process-service"

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
// authentication, every other route only with a service token that has
// api.TransferServiceScope. The internal API trusts its caller for the
// participant of a transfer, so a user or participant token must not reach
// it, even with the scope.
func newServer(v auth.Verifier) http.Handler {
	internal := http.NewServeMux()
	registerInternalRoutes(internal)
	return internalapi.Handler(internalapi.Config{
		Verifier: v,
		Scope:    api.TransferServiceScope,
		Kinds:    []auth.Kind{auth.KindService},
		Realm:    internalAuthRealm,
		OnReject: logInternalAuthFailure,
	}, healthHandler, internal)
}

func logInternalAuthFailure(r *http.Request, err error) {
	// err comes from pkg/auth and never contains the token.
	logger.Sugar().Infow("internal API request rejected", "method", r.Method, "path", r.URL.Path, "status", auth.StatusCode(err), "reason", err.Error())
}

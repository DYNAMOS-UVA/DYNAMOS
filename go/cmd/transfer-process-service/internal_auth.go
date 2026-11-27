package main

import (
	"fmt"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
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

// internalAuth lets a request through only with a valid service token that
// has api.TransferServiceScope. The internal API trusts its caller for the
// participant of a transfer, so a user or participant token must not reach
// it, even with the scope.
func internalAuth(v auth.Verifier) func(http.Handler) http.Handler {
	opts := []auth.MiddlewareOption{auth.WithRealm(internalAuthRealm), auth.WithErrorHandler(logInternalAuthFailure)}
	authenticate := auth.Middleware(v, opts...)
	authorize := auth.RequireScope([]string{api.TransferServiceScope}, opts...)
	return func(next http.Handler) http.Handler {
		serviceOnly := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := auth.PrincipalFrom(r.Context())
			if p.Kind != auth.KindService {
				logInternalAuthFailure(w, r, fmt.Errorf("%w: %s token on the internal API", auth.ErrForbidden, p.Kind))
				return
			}
			next.ServeHTTP(w, r)
		})
		return authenticate(authorize(serviceOnly))
	}
}

func logInternalAuthFailure(w http.ResponseWriter, r *http.Request, err error) {
	// err comes from pkg/auth and never contains the token.
	logger.Sugar().Infow("internal API request rejected", "method", r.Method, "path", r.URL.Path, "status", auth.StatusCode(err), "reason", err.Error())
	auth.DefaultErrorHandler(internalAuthRealm)(w, r, err)
}

// newServer returns the HTTP handler of the service: /health without
// authentication, every other route behind internalAuth.
func newServer(v auth.Verifier) http.Handler {
	internal := http.NewServeMux()
	registerInternalRoutes(internal)

	root := http.NewServeMux()
	root.HandleFunc("/health", healthHandler)
	root.Handle("/", internalAuth(v)(internal))
	return root
}

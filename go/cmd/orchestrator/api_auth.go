package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"

	// Providers the orchestrator API can be configured with.
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/oidc"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/staticdev"
)

// apiAuthEnvPrefix selects the verifiers for callers of the orchestrator
// HTTP API (operators and tooling), for example:
//
//	AUTH_PROVIDERS=oidc
//	AUTH_OIDC_ISSUER=https://login.microsoftonline.com/<tenant>/v2.0
//	AUTH_OIDC_AUDIENCE=api://dynamos-orchestrator
//
// This is separate from TOKEN_ISSUER_*, which configures the job tokens
// the orchestrator mints for agents.
const apiAuthEnvPrefix = "AUTH"

const apiAuthRealm = "dynamos-orchestrator"

// Default scopes. ORCHESTRATOR_READ_SCOPE and ORCHESTRATOR_WRITE_SCOPE
// override them, so they can match what an IdP can put in a token (for
// example Entra app role values).
const (
	defaultReadScope  = "orchestrator:read"
	defaultWriteScope = "orchestrator:write"
)

// apiScopes are the scopes the orchestrator API requires.
type apiScopes struct {
	read  string
	write string
}

func apiScopesFromEnv() apiScopes {
	s := apiScopes{read: defaultReadScope, write: defaultWriteScope}
	if v := strings.TrimSpace(os.Getenv("ORCHESTRATOR_READ_SCOPE")); v != "" {
		s.read = v
	}
	if v := strings.TrimSpace(os.Getenv("ORCHESTRATOR_WRITE_SCOPE")); v != "" {
		s.write = v
	}
	return s
}

// writeOnlyPaths change state even on GET, so they always need the write
// scope. /updateEtc reloads all policy-enforcer configuration.
var writeOnlyPaths = map[string]bool{"/updateEtc": true}

// required returns the scope r needs. GET and HEAD read, every other
// method writes (PUT replaces agreements, archetypes, request types and
// microservice metadata in etcd).
func (s apiScopes) required(r *http.Request) string {
	if writeOnlyPaths[r.URL.Path] {
		return s.write
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return s.read
	default:
		return s.write
	}
}

// allows reports whether p may make r. The write scope includes read.
func (s apiScopes) allows(p auth.Principal, r *http.Request) bool {
	need := s.required(r)
	if need == s.read {
		return p.HasScope(s.read) || p.HasScope(s.write)
	}
	return p.HasScope(s.write)
}

func mustBuildAPIVerifier() auth.Verifier {
	v, err := auth.VerifierFromEnv(apiAuthEnvPrefix)
	if err != nil {
		logger.Sugar().Fatalf("Failed to configure API authentication: %v", err)
	}
	return v
}

// apiAuth authenticates every API request and checks its scope. It wraps
// the mux after StripPrefix, so paths are relative to the API version.
func apiAuth(v auth.Verifier, scopes apiScopes) func(http.Handler) http.Handler {
	authenticate := auth.Middleware(v, auth.WithRealm(apiAuthRealm), auth.WithErrorHandler(logAPIAuthFailure))
	return func(next http.Handler) http.Handler {
		authorize := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := auth.PrincipalFrom(r.Context())
			if !scopes.allows(p, r) {
				logAPIAuthFailure(w, r, fmt.Errorf("%w: %s %s needs scope %q", auth.ErrForbidden, r.Method, r.URL.Path, scopes.required(r)))
				return
			}
			next.ServeHTTP(w, r)
		})
		return authenticate(authorize)
	}
}

func logAPIAuthFailure(w http.ResponseWriter, r *http.Request, err error) {
	// err comes from pkg/auth and never contains the token.
	logger.Sugar().Infow("API request rejected", "method", r.Method, "path", r.URL.Path, "status", auth.StatusCode(err), "reason", err.Error())
	auth.DefaultErrorHandler(apiAuthRealm)(w, r, err)
}

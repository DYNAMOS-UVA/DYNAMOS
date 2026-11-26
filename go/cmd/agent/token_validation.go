package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
)

// authEnvPrefix selects the verifier from the environment, for example:
//
//	AUTH_PROVIDERS=jwt
//	AUTH_JWT_ISSUER=dynamos-orchestrator
//	AUTH_JWT_PUBLIC_KEY_FILES=/etc/dynamos/auth/orchestrator.pub.pem
//
// AUTH_JWT_AUDIENCE defaults to this agent's DATA_STEWARD_NAME, so a token
// minted for another steward is rejected.
const authEnvPrefix = "AUTH"

// sqlDataRequestType is the request type of the agent's HTTP route.
const sqlDataRequestType = "sqlDataRequest"

// buildRequestVerifier builds the verifier for incoming data requests.
// It must run after serviceName is set.
func buildRequestVerifier(environ []string) (auth.Verifier, error) {
	specs, err := auth.SpecsFromEnviron(authEnvPrefix, environ)
	if err != nil {
		return nil, err
	}
	for i := range specs {
		if specs[i].Name == jwtauth.Name && specs[i].Settings.Get("audience") == "" {
			if serviceName == "" {
				return nil, fmt.Errorf("%w: DATA_STEWARD_NAME is empty, cannot default the token audience", auth.ErrProviderConfig)
			}
			specs[i].Settings["audience"] = serviceName
		}
	}
	return auth.DefaultRegistry.BuildAll(specs...)
}

func mustBuildRequestVerifier() auth.Verifier {
	v, err := buildRequestVerifier(os.Environ())
	if err != nil {
		logger.Sugar().Fatalf("Failed to configure request authentication: %v", err)
	}
	return v
}

// authMiddleware authenticates the caller and requires the scope for
// requestType. The token is never logged.
func authMiddleware(v auth.Verifier, requestType string) func(http.Handler) http.Handler {
	opts := []auth.MiddlewareOption{auth.WithRealm("dynamos-agent"), auth.WithErrorHandler(logAuthFailure)}
	authenticate := auth.Middleware(v, opts...)
	authorize := auth.RequireScope([]string{api.AgentRequestScope(requestType)}, opts...)
	return func(next http.Handler) http.Handler {
		return authenticate(authorize(next))
	}
}

func logAuthFailure(w http.ResponseWriter, r *http.Request, err error) {
	// err comes from pkg/auth and never contains the token.
	logger.Sugar().Infow("request rejected", "path", r.URL.Path, "status", auth.StatusCode(err), "reason", err.Error())
	auth.DefaultErrorHandler("dynamos-agent")(w, r, err)
}

// authorizeJob checks that the verified token was minted for this job and
// this user. The middleware already checked signature, audience and scope;
// this binds the token to the request body, so a valid token for one job
// cannot run another job or act for another user.
func authorizeJob(ctx context.Context, userName, jobID string) error {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return fmt.Errorf("%w: request is not authenticated", auth.ErrMissingCredentials)
	}
	if userName == "" || p.Subject != userName {
		return fmt.Errorf("%w: token subject does not match the request user", auth.ErrForbidden)
	}
	if tokenJob, ok := p.Attribute(api.JobIDAttribute); !ok || jobID == "" || tokenJob != jobID {
		return fmt.Errorf("%w: token is not for this job", auth.ErrForbidden)
	}
	return nil
}

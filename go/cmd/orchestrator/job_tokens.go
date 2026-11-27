package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
)

// tokenIssuerEnvPrefix selects the issuer from the environment, for example:
//
//	TOKEN_ISSUER_PROVIDER=jwt
//	TOKEN_ISSUER_JWT_ISSUER=dynamos-orchestrator
//	TOKEN_ISSUER_JWT_PRIVATE_KEY_FILE=/etc/dynamos/auth/orchestrator.key.pem
//
// See auth.IssuerFromEnv and the jwtauth package for all settings.
const tokenIssuerEnvPrefix = "TOKEN_ISSUER"

// jobTokenTTL bounds how long a job token stays usable. api-gateway sends
// the data requests right after it receives the approval, so a short
// lifetime is enough and limits replay.
const jobTokenTTL = 5 * time.Minute

// tokenIssuer mints the per-target job tokens. Set once in main.
var tokenIssuer auth.Issuer

// issueProviderTokens mints one token for each target in targets (the map
// api-gateway receives as authorized_providers). Each token is bound to:
//
//   - aud: the target name, so the token only works at that agent
//   - sub: the user the job runs for
//   - attrs.jobId: the job, so it cannot start another job of that user
//   - scope: the request type the agent route serves
//
// It fails as a whole when one token cannot be minted: a partial set
// would make api-gateway call some agents without credentials.
func issueProviderTokens(ctx context.Context, issuer auth.Issuer, userName, jobID, requestType string, targets map[string]string) (map[string]string, error) {
	if issuer == nil {
		return nil, errors.New("no token issuer configured")
	}
	if userName == "" || jobID == "" || requestType == "" {
		return nil, errors.New("user name, job ID and request type are required")
	}
	subject := auth.Principal{Subject: userName, Kind: subjectKind(userName)}
	tokens := make(map[string]string, len(targets))
	for target := range targets {
		tok, err := issuer.Issue(ctx, subject, auth.Grant{
			Audience:   []string{target},
			Scopes:     []string{api.AgentRequestScope(requestType)},
			TTL:        jobTokenTTL,
			Attributes: map[string]string{api.JobIDAttribute: jobID},
		})
		if err != nil {
			return nil, fmt.Errorf("issuing token for target %q: %w", target, err)
		}
		tokens[target] = tok.Value
	}
	return tokens, nil
}

// subjectKind tells DSP participants (identified by DID, see
// transfer-process-service) from end users.
func subjectKind(userName string) auth.Kind {
	if strings.HasPrefix(userName, "did:") {
		return auth.KindParticipant
	}
	return auth.KindUser
}

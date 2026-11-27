package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
)

// gatewayTokenEnvPrefix selects the issuer for the tokens this service
// sends to api-gateway, for example:
//
//	TOKEN_ISSUER_PROVIDER=jwt
//	TOKEN_ISSUER_JWT_ISSUER=dynamos-transfer-process-service
//	TOKEN_ISSUER_JWT_PRIVATE_KEY_FILE=/etc/dynamos/auth/transfer-process-service.key.pem
//
// api-gateway must trust the matching public key (its AUTH_JWT_* settings).
const gatewayTokenEnvPrefix = "TOKEN_ISSUER"

// gatewayAudience is the "aud" api-gateway expects. GATEWAY_TOKEN_AUDIENCE
// overrides it.
var gatewayAudience = "api-gateway"

// gatewayTokenTTL covers one job request. The gateway checks the token on
// entry only, so the 130s job timeout does not need to fit inside it.
const gatewayTokenTTL = 2 * time.Minute

// gatewayTokenIssuer mints the tokens. Nil when not configured: job
// requests then fail with errNoGatewayIssuer instead of going out
// unauthenticated.
var gatewayTokenIssuer auth.Issuer

var errNoGatewayIssuer = errors.New("no api-gateway token issuer configured (set TOKEN_ISSUER_PROVIDER)")

// configureGatewayAuth loads the issuer. A missing configuration is not
// fatal: deployments that never run jobs (for example the DSP TCK run)
// still start, and log why job requests will fail.
func configureGatewayAuth() {
	if v := os.Getenv("GATEWAY_TOKEN_AUDIENCE"); v != "" {
		gatewayAudience = v
	}
	if strings.TrimSpace(os.Getenv(gatewayTokenEnvPrefix+"_PROVIDER")) == "" {
		logger.Sugar().Warnw("api-gateway token issuer not configured; job requests will fail", "env", gatewayTokenEnvPrefix+"_PROVIDER")
		return
	}
	iss, err := auth.IssuerFromEnv(gatewayTokenEnvPrefix)
	if err != nil {
		// Configured but broken is a deployment error: fail loudly.
		logger.Sugar().Fatalf("Failed to configure api-gateway token issuer: %v", err)
	}
	gatewayTokenIssuer = iss
}

// gatewayToken mints a token that lets api-gateway start a job for
// participant, the DSP participant this service already verified.
func gatewayToken(ctx context.Context, participant string) (string, error) {
	if gatewayTokenIssuer == nil {
		return "", errNoGatewayIssuer
	}
	if participant == "" {
		return "", errors.New("transfer has no participant to act for")
	}
	tok, err := gatewayTokenIssuer.Issue(ctx,
		auth.Principal{Subject: participant, Kind: auth.KindParticipant},
		auth.Grant{
			Audience: []string{gatewayAudience},
			Scopes:   []string{api.GatewayRequestScope},
			TTL:      gatewayTokenTTL,
		})
	if err != nil {
		return "", fmt.Errorf("minting api-gateway token: %w", err)
	}
	return tok.Value, nil
}

package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	_ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
)

// internalTokenEnvPrefix selects the issuer for the service tokens this
// connector sends to the internal APIs of the DSP services, for example:
//
//	TOKEN_ISSUER_PROVIDER=jwt
//	TOKEN_ISSUER_JWT_ISSUER=dynamos-dsp-connector
//	TOKEN_ISSUER_JWT_PRIVATE_KEY_FILE=/etc/dynamos/auth/dsp-connector.key.pem
//
// The receiving services trust the matching public key (their AUTH_JWT_*
// settings).
const internalTokenEnvPrefix = "TOKEN_ISSUER"

// internalTokenTTL covers one internal call. A new token is minted for
// each call, so it does not need to be longer.
const internalTokenTTL = time.Minute

// transferServiceAudience is the "aud" transfer-process-service expects.
// TRANSFER_SERVICE_AUDIENCE overrides it.
var transferServiceAudience = "transfer-process-service"

// internalTokenIssuer mints the tokens. configureInternalAuth sets it at
// startup. Nil makes every internal call fail with errNoInternalIssuer.
var internalTokenIssuer auth.Issuer

var errNoInternalIssuer = errors.New("no internal service token issuer configured (set TOKEN_ISSUER_PROVIDER)")

// configureInternalAuth loads the issuer. The connector cannot do its work
// without it, so a missing or broken configuration stops the process.
func configureInternalAuth() {
	if v := os.Getenv("TRANSFER_SERVICE_AUDIENCE"); v != "" {
		transferServiceAudience = v
	}
	iss, err := auth.IssuerFromEnv(internalTokenEnvPrefix)
	if err != nil {
		logger.Sugar().Fatalf("Failed to configure internal service token issuer: %v", err)
	}
	internalTokenIssuer = iss
}

// serviceTokenTransport adds a fresh service token to each request. The
// token is for one audience and carries one scope.
type serviceTokenTransport struct {
	base     http.RoundTripper
	audience func() string
	scope    string
}

func (t serviceTokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if internalTokenIssuer == nil {
		closeBody(r)
		return nil, errNoInternalIssuer
	}
	tok, err := internalTokenIssuer.Issue(r.Context(),
		auth.Principal{Subject: api.DSPConnectorSubject, Kind: auth.KindService},
		auth.Grant{
			Audience: []string{t.audience()},
			Scopes:   []string{t.scope},
			TTL:      internalTokenTTL,
		})
	if err != nil {
		closeBody(r)
		return nil, fmt.Errorf("minting internal service token: %w", err)
	}
	// RoundTrip must not change the caller's request.
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+tok.Value)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// closeBody follows the RoundTripper contract: the body is closed also
// when the request is not sent.
func closeBody(r *http.Request) {
	if r.Body != nil {
		r.Body.Close()
	}
}

// internalServiceClient returns an HTTP client that authenticates to one
// internal DSP service.
func internalServiceClient(audience func() string, scope string) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: serviceTokenTransport{audience: audience, scope: scope},
	}
}

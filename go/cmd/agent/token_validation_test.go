package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOrchestratorIssuer = "dynamos-orchestrator"

// setupAgentAuth sets serviceName to UVA, writes an orchestrator key pair
// and returns the issuer plus the environ an agent would see.
func setupAgentAuth(t *testing.T) (*jwtauth.Issuer, []string) {
	t.Helper()
	saved := serviceName
	t.Cleanup(func() { serviceName = saved })
	serviceName = "UVA"

	key, err := jwtauth.GenerateKey()
	require.NoError(t, err)
	iss, err := jwtauth.NewIssuer(testOrchestratorIssuer, key)
	require.NoError(t, err)
	pubPEM, err := jwtauth.MarshalPublicKeyPEM(iss.PublicKey())
	require.NoError(t, err)
	pubFile := filepath.Join(t.TempDir(), "orchestrator.pub.pem")
	require.NoError(t, os.WriteFile(pubFile, pubPEM, 0o644))

	return iss, []string{
		"AUTH_PROVIDERS=jwt",
		"AUTH_JWT_ISSUER=" + testOrchestratorIssuer,
		"AUTH_JWT_PUBLIC_KEY_FILES=" + pubFile,
	}
}

func jobToken(t *testing.T, iss *jwtauth.Issuer, user, aud, jobID, scope string) string {
	t.Helper()
	tok, err := iss.Issue(context.Background(), auth.Principal{Subject: user, Kind: auth.KindUser}, auth.Grant{
		Audience:   []string{aud},
		Scopes:     []string{scope},
		TTL:        time.Minute,
		Attributes: map[string]string{api.JobIDAttribute: jobID},
	})
	require.NoError(t, err)
	return tok.Value
}

// handlerUnderTest mirrors main(): middleware, then authorizeJob in the
// handler with the user and job from the request.
func handlerUnderTest(v auth.Verifier, user, jobID string) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := authorizeJob(r.Context(), user, jobID); err != nil {
			logAuthFailure(w, r, err)
			return
		}
		io.WriteString(w, "ok")
	})
	return authMiddleware(v, sqlDataRequestType)(inner)
}

func call(h http.Handler, token string) int {
	req := httptest.NewRequest(http.MethodPost, "/agent/v1/sqlDataRequest/uva", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestAgentAuth(t *testing.T) {
	iss, environ := setupAgentAuth(t)
	v, err := buildRequestVerifier(environ)
	require.NoError(t, err)
	scope := api.AgentRequestScope(sqlDataRequestType)
	h := handlerUnderTest(v, "user@uva.nl", "job-1")

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"valid token", jobToken(t, iss, "user@uva.nl", "UVA", "job-1", scope), http.StatusOK},
		{"no token", "", http.StatusUnauthorized},
		{"old hardcoded token", "1234", http.StatusUnauthorized},
		{"token for another steward", jobToken(t, iss, "user@uva.nl", "VU", "job-1", scope), http.StatusUnauthorized},
		{"token for another job", jobToken(t, iss, "user@uva.nl", "UVA", "job-2", scope), http.StatusForbidden},
		{"token for another user", jobToken(t, iss, "other@uva.nl", "UVA", "job-1", scope), http.StatusForbidden},
		{"wrong scope", jobToken(t, iss, "user@uva.nl", "UVA", "job-1", "agent:other"), http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, call(h, c.token))
		})
	}

	t.Run("token from another issuer key", func(t *testing.T) {
		other, _ := setupAgentAuth(t)
		assert.Equal(t, http.StatusUnauthorized, call(h, jobToken(t, other, "user@uva.nl", "UVA", "job-1", scope)))
	})
}

func TestAgentAuthAudienceOverride(t *testing.T) {
	iss, environ := setupAgentAuth(t)
	v, err := buildRequestVerifier(append(environ, "AUTH_JWT_AUDIENCE=uva-agent"))
	require.NoError(t, err)
	h := handlerUnderTest(v, "u", "j")
	scope := api.AgentRequestScope(sqlDataRequestType)
	assert.Equal(t, http.StatusOK, call(h, jobToken(t, iss, "u", "uva-agent", "j", scope)))
	assert.Equal(t, http.StatusUnauthorized, call(h, jobToken(t, iss, "u", "UVA", "j", scope)))
}

func TestBuildRequestVerifierErrors(t *testing.T) {
	_, environ := setupAgentAuth(t)

	_, err := buildRequestVerifier(nil)
	assert.ErrorIs(t, err, auth.ErrProviderConfig, "no providers must not start an open agent")

	serviceName = ""
	_, err = buildRequestVerifier(environ)
	assert.ErrorIs(t, err, auth.ErrProviderConfig, "no audience and no steward name")
}

func TestAuthorizeJobWithoutPrincipal(t *testing.T) {
	err := authorizeJob(context.Background(), "u", "j")
	assert.ErrorIs(t, err, auth.ErrMissingCredentials)

	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "u"})
	assert.ErrorIs(t, authorizeJob(ctx, "u", "j"), auth.ErrForbidden, "no jobId attribute")
	assert.ErrorIs(t, authorizeJob(ctx, "", "j"), auth.ErrForbidden, "empty user")
}

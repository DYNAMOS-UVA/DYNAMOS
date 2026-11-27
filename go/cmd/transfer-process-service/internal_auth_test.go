package main

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testConnectorIssuerName = "dynamos-dsp-connector"
	testInternalAudience    = "transfer-process-service"
)

// newTestInternalServer returns the real server handler with a verifier
// that trusts connector, as the chart configures it.
func newTestInternalServer(t *testing.T, connector *jwtauth.Issuer) http.Handler {
	t.Helper()
	v, err := jwtauth.NewVerifier(testConnectorIssuerName, []ed25519.PublicKey{connector.PublicKey()}, []string{testInternalAudience})
	require.NoError(t, err)
	return newServer(v)
}

func newTestConnectorIssuer(t *testing.T) *jwtauth.Issuer {
	t.Helper()
	key, err := jwtauth.GenerateKey()
	require.NoError(t, err)
	iss, err := jwtauth.NewIssuer(testConnectorIssuerName, key)
	require.NoError(t, err)
	return iss
}

func mintInternalToken(t *testing.T, iss *jwtauth.Issuer, kind auth.Kind, aud string, scopes ...string) string {
	t.Helper()
	tok, err := iss.Issue(context.Background(),
		auth.Principal{Subject: api.DSPConnectorSubject, Kind: kind},
		auth.Grant{Audience: []string{aud}, Scopes: scopes, TTL: time.Minute})
	require.NoError(t, err)
	return tok.Value
}

// serve sends a POST with an invalid JSON body to the transfer
// collection. A request that passes the auth check stops at the body check
// (400) before it reaches the store, so the test needs no etcd.
func serve(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("not json"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestInternalAuth_HealthIsOpen(t *testing.T) {
	h := newTestInternalServer(t, newTestConnectorIssuer(t))

	rec := serve(h, http.MethodGet, "/health", "")

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestInternalAuth_AcceptsConnectorServiceToken(t *testing.T) {
	connector := newTestConnectorIssuer(t)
	h := newTestInternalServer(t, connector)
	tok := mintInternalToken(t, connector, auth.KindService, testInternalAudience, api.TransferServiceScope)

	for _, path := range []string{"/internal/v1/transfers", "/internal/v1/transfers/consumer"} {
		rec := serve(h, http.MethodPost, path, tok)
		// The request reached the handler: it answers on the body.
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "invalid-body", path)
	}
}

func TestInternalAuth_RejectsBadCallers(t *testing.T) {
	connector := newTestConnectorIssuer(t)
	other := newTestConnectorIssuer(t) // same issuer name, key not trusted
	h := newTestInternalServer(t, connector)

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"not a JWT", "not-a-token", http.StatusUnauthorized},
		{"untrusted key", mintInternalToken(t, other, auth.KindService, testInternalAudience, api.TransferServiceScope), http.StatusUnauthorized},
		{"other audience", mintInternalToken(t, connector, auth.KindService, "api-gateway", api.TransferServiceScope), http.StatusUnauthorized},
		{"missing scope", mintInternalToken(t, connector, auth.KindService, testInternalAudience, api.GatewayRequestScope), http.StatusForbidden},
		{"participant kind", mintInternalToken(t, connector, auth.KindParticipant, testInternalAudience, api.TransferServiceScope), http.StatusForbidden},
		{"user kind", mintInternalToken(t, connector, auth.KindUser, testInternalAudience, api.TransferServiceScope), http.StatusForbidden},
	}
	paths := []struct{ method, path string }{
		{http.MethodPost, "/internal/v1/transfers"},
		{http.MethodGet, "/internal/v1/transfers/urn:dynamos:transfer:VU:x"},
		{http.MethodPost, "/internal/v1/transfers/urn:dynamos:transfer:VU:x/start"},
		{http.MethodPost, "/internal/v1/transfers/consumer"},
		{http.MethodGet, "/internal/v1/transfers/consumer/urn:dynamos:transfer:VU:x"},
		{http.MethodGet, "/no/such/route"},
	}
	for _, c := range cases {
		for _, p := range paths {
			rec := serve(h, p.method, p.path, c.token)
			assert.Equal(t, c.want, rec.Code, "%s: %s %s", c.name, p.method, p.path)
			assert.NotContains(t, rec.Body.String(), "transfer", "%s: the error must not come from a handler", c.name)
		}
	}
}

func TestInternalAuth_DenyAllWithoutVerifier(t *testing.T) {
	connector := newTestConnectorIssuer(t)
	h := newServer(nil)
	tok := mintInternalToken(t, connector, auth.KindService, testInternalAudience, api.TransferServiceScope)

	rec := serve(h, http.MethodPost, "/internal/v1/transfers", tok)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

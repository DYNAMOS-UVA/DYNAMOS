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

const testConnectorIssuerName = "dynamos-dsp-connector"

func newTestNegotiationServer(t *testing.T) (http.Handler, *jwtauth.Issuer) {
	t.Helper()
	key, err := jwtauth.GenerateKey()
	require.NoError(t, err)
	iss, err := jwtauth.NewIssuer(testConnectorIssuerName, key)
	require.NoError(t, err)
	v, err := jwtauth.NewVerifier(testConnectorIssuerName, []ed25519.PublicKey{iss.PublicKey()}, []string{"negotiation-service"})
	require.NoError(t, err)
	return newServer(v), iss
}

func mintNegotiationToken(t *testing.T, iss *jwtauth.Issuer, kind auth.Kind, aud string, scopes ...string) string {
	t.Helper()
	tok, err := iss.Issue(context.Background(),
		auth.Principal{Subject: "caller", Kind: kind},
		auth.Grant{Audience: []string{aud}, Scopes: scopes, TTL: time.Minute})
	require.NoError(t, err)
	return tok.Value
}

// serveNegotiation sends a POST with an invalid JSON body. A request that
// passes the auth rule stops at the body check (400) before the store, so
// the test needs no etcd.
func serveNegotiation(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("not json"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNegotiationInternalAuth_HealthIsOpen(t *testing.T) {
	h, _ := newTestNegotiationServer(t)
	assert.Equal(t, http.StatusOK, serveNegotiation(h, http.MethodGet, "/health", "").Code)
}

func TestNegotiationInternalAuth_AcceptsServiceAndUser(t *testing.T) {
	h, iss := newTestNegotiationServer(t)
	for _, kind := range []auth.Kind{auth.KindService, auth.KindUser} {
		tok := mintNegotiationToken(t, iss, kind, "negotiation-service", api.NegotiationServiceScope)
		for _, path := range []string{"/internal/v1/negotiations", "/internal/v1/negotiations/consumer"} {
			rec := serveNegotiation(h, http.MethodPost, path, tok)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s %s", kind, path)
			assert.Contains(t, rec.Body.String(), "invalid-body", "%s %s: the handler must answer", kind, path)
		}
	}
}

func TestNegotiationInternalAuth_RejectsBadCallers(t *testing.T) {
	h, iss := newTestNegotiationServer(t)
	key, err := jwtauth.GenerateKey()
	require.NoError(t, err)
	untrusted, err := jwtauth.NewIssuer(testConnectorIssuerName, key)
	require.NoError(t, err)

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"untrusted key", mintNegotiationToken(t, untrusted, auth.KindService, "negotiation-service", api.NegotiationServiceScope), http.StatusUnauthorized},
		{"transfer-process-service audience", mintNegotiationToken(t, iss, auth.KindService, "transfer-process-service", api.NegotiationServiceScope), http.StatusUnauthorized},
		{"transfer scope only", mintNegotiationToken(t, iss, auth.KindService, "negotiation-service", api.TransferServiceScope), http.StatusForbidden},
		{"participant kind", mintNegotiationToken(t, iss, auth.KindParticipant, "negotiation-service", api.NegotiationServiceScope), http.StatusForbidden},
	}
	paths := []struct{ method, path string }{
		{http.MethodPost, "/internal/v1/negotiations"},
		{http.MethodGet, "/internal/v1/negotiations/urn:dynamos:negotiation:VU:x"},
		{http.MethodGet, "/internal/v1/negotiations/by-agreement/urn:uuid:a"},
		{http.MethodPost, "/internal/v1/negotiations/urn:dynamos:negotiation:VU:x/events"},
		{http.MethodPost, "/internal/v1/negotiations/urn:dynamos:negotiation:VU:x/agreement"},
		{http.MethodPost, "/internal/v1/negotiations/consumer"},
		{http.MethodPost, "/internal/v1/negotiations/consumer/x/accept"},
		{http.MethodGet, "/no/such/route"},
	}
	for _, c := range cases {
		for _, p := range paths {
			rec := serveNegotiation(h, p.method, p.path, c.token)
			assert.Equal(t, c.want, rec.Code, "%s: %s %s", c.name, p.method, p.path)
		}
	}
}

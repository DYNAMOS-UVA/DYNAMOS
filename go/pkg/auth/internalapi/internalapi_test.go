package internalapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/stretchr/testify/assert"
)

// fakeVerifier accepts the token "ok-<kind>" with the scope "s".
var fakeVerifier = auth.VerifierFunc(func(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "ok-service":
		return auth.Principal{Subject: "a", Kind: auth.KindService, Scopes: []string{"s"}}, nil
	case "ok-user":
		return auth.Principal{Subject: "a", Kind: auth.KindUser, Scopes: []string{"s"}}, nil
	case "ok-participant":
		return auth.Principal{Subject: "a", Kind: auth.KindParticipant, Scopes: []string{"s"}}, nil
	case "no-scope":
		return auth.Principal{Subject: "a", Kind: auth.KindService}, nil
	}
	return auth.Principal{}, auth.ErrInvalidCredentials
})

func serve(h http.Handler, path, token string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestHandler(t *testing.T) {
	var rejected []error
	internal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	health := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	cases := []struct {
		name  string
		kinds []auth.Kind
		path  string
		token string
		want  int
	}{
		{"health without token", nil, "/health", "", http.StatusOK},
		{"no token", nil, "/internal/v1/x", "", http.StatusUnauthorized},
		{"bad token", nil, "/internal/v1/x", "bad", http.StatusUnauthorized},
		{"no scope", nil, "/internal/v1/x", "no-scope", http.StatusForbidden},
		{"service, default kinds", nil, "/internal/v1/x", "ok-service", http.StatusTeapot},
		{"user, default kinds", nil, "/internal/v1/x", "ok-user", http.StatusForbidden},
		{"user allowed", []auth.Kind{auth.KindService, auth.KindUser}, "/internal/v1/x", "ok-user", http.StatusTeapot},
		{"participant never by default", []auth.Kind{auth.KindService, auth.KindUser}, "/internal/v1/x", "ok-participant", http.StatusForbidden},
		{"unknown path still protected", nil, "/other", "", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rejected = nil
			h := Handler(Config{Verifier: fakeVerifier, Scope: "s", Kinds: c.kinds, Realm: "test",
				OnReject: func(_ *http.Request, err error) { rejected = append(rejected, err) }}, health, internal)
			got := serve(h, c.path, c.token)
			assert.Equal(t, c.want, got)
			if got == http.StatusUnauthorized || got == http.StatusForbidden {
				assert.Len(t, rejected, 1, "OnReject must be called once")
			} else {
				assert.Empty(t, rejected)
			}
		})
	}
}

func TestHandler_NilVerifierDeniesAll(t *testing.T) {
	h := Handler(Config{Scope: "s", Realm: "test"}, func(http.ResponseWriter, *http.Request) {}, http.NotFoundHandler())
	assert.Equal(t, http.StatusUnauthorized, serve(h, "/internal/v1/x", "ok-service"))
}

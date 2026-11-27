package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/stretchr/testify/assert"
)

// scopeVerifier maps fixed test tokens to principals with given scopes.
var scopeVerifier = auth.VerifierFunc(func(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "reader":
		return auth.Principal{Subject: "reader", Scopes: []string{defaultReadScope}}, nil
	case "writer":
		return auth.Principal{Subject: "writer", Scopes: []string{defaultWriteScope}}, nil
	case "nobody":
		return auth.Principal{Subject: "nobody"}, nil
	case "custom":
		return auth.Principal{Subject: "custom", Scopes: []string{"Orchestrator.Admin"}}, nil
	}
	return auth.Principal{}, auth.ErrInvalidCredentials
})

func apiCall(h http.Handler, method, path, token string) int {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestOrchestratorAPIAuth(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := apiAuth(scopeVerifier, apiScopes{read: defaultReadScope, write: defaultWriteScope})(next)

	cases := []struct {
		method, path, token string
		want                int
	}{
		{http.MethodGet, "/archetypes", "", http.StatusUnauthorized},
		{http.MethodPut, "/policyEnforcer/agreements", "", http.StatusUnauthorized},
		{http.MethodPut, "/policyEnforcer/agreements", "1234", http.StatusUnauthorized},
		{http.MethodGet, "/archetypes", "nobody", http.StatusForbidden},

		{http.MethodGet, "/archetypes", "reader", http.StatusOK},
		{http.MethodHead, "/microservices", "reader", http.StatusOK},
		{http.MethodPut, "/archetypes", "reader", http.StatusForbidden},
		{http.MethodPost, "/requestTypes", "reader", http.StatusForbidden},
		{http.MethodGet, "/updateEtc", "reader", http.StatusForbidden},

		{http.MethodGet, "/archetypes", "writer", http.StatusOK},
		{http.MethodPut, "/policyEnforcer/agreements", "writer", http.StatusOK},
		{http.MethodGet, "/updateEtc", "writer", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path+" as "+c.token, func(t *testing.T) {
			reached = false
			got := apiCall(h, c.method, c.path, c.token)
			assert.Equal(t, c.want, got)
			assert.Equal(t, got == http.StatusOK, reached, "handler runs only when allowed")
		})
	}
}

func TestOrchestratorAPIScopesFromEnv(t *testing.T) {
	t.Setenv("ORCHESTRATOR_READ_SCOPE", "")
	t.Setenv("ORCHESTRATOR_WRITE_SCOPE", "")
	assert.Equal(t, apiScopes{read: defaultReadScope, write: defaultWriteScope}, apiScopesFromEnv())

	t.Setenv("ORCHESTRATOR_WRITE_SCOPE", "Orchestrator.Admin")
	s := apiScopesFromEnv()
	assert.Equal(t, "Orchestrator.Admin", s.write)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := apiAuth(scopeVerifier, s)(ok)
	assert.Equal(t, http.StatusOK, apiCall(h, http.MethodPut, "/archetypes", "custom"))
	assert.Equal(t, http.StatusForbidden, apiCall(h, http.MethodPut, "/archetypes", "writer"),
		"default write scope no longer accepted once overridden")
}

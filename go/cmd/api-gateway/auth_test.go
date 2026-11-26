package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestingUser(t *testing.T) {
	ctx := context.Background()
	_, err := requestingUser(ctx)
	assert.ErrorIs(t, err, auth.ErrMissingCredentials)

	user, err := requestingUser(auth.WithPrincipal(ctx, auth.Principal{Subject: "user@uva.nl", Kind: auth.KindUser}))
	require.NoError(t, err)
	assert.Equal(t, "user@uva.nl", user)

	_, err = requestingUser(auth.WithPrincipal(ctx, auth.Principal{Subject: "did:web:x", Kind: auth.KindParticipant}))
	assert.ErrorIs(t, err, auth.ErrForbidden, "participant token without the gateway scope")

	_, err = requestingUser(auth.WithPrincipal(ctx, auth.Principal{Subject: "svc", Kind: auth.KindService}))
	assert.ErrorIs(t, err, auth.ErrForbidden, "service token without the gateway scope")

	user, err = requestingUser(auth.WithPrincipal(ctx, auth.Principal{
		Subject: "did:web:x", Kind: auth.KindParticipant, Scopes: []string{api.GatewayRequestScope},
	}))
	require.NoError(t, err)
	assert.Equal(t, "did:web:x", user)
}

func TestCallerAuthMiddleware(t *testing.T) {
	key, _ := jwtauth.GenerateKey()
	iss, err := jwtauth.NewIssuer("dynamos-transfer-process-service", key)
	require.NoError(t, err)
	v, err := jwtauth.NewVerifier("dynamos-transfer-process-service", []ed25519.PublicKey{iss.PublicKey()}, []string{"api-gateway"})
	require.NoError(t, err)

	h := callerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := requestingUser(r.Context())
		if err != nil {
			logAuthFailure(w, r, err)
			return
		}
		io.WriteString(w, user)
	}))
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/requestApproval", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	mint := func(scopes ...string) string {
		tok, err := iss.Issue(context.Background(), auth.Principal{Subject: "did:web:consumer", Kind: auth.KindParticipant},
			auth.Grant{Audience: []string{"api-gateway"}, Scopes: scopes, TTL: time.Minute})
		require.NoError(t, err)
		return tok.Value
	}

	assert.Equal(t, http.StatusUnauthorized, call("").Code)
	assert.Equal(t, http.StatusUnauthorized, call("1234").Code, "old hardcoded token")
	assert.Equal(t, http.StatusForbidden, call(mint()).Code, "no gateway scope")

	rec := call(mint(api.GatewayRequestScope))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "did:web:consumer", rec.Body.String())
}

func TestSendDataUsesProviderToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	body, err := sendData(srv.URL, []byte(`{}`), "tok-uva")
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, body)
	assert.Equal(t, "Bearer tok-uva", gotAuth)
}

func TestSendDataToAuthProvidersWithoutTokenFails(t *testing.T) {
	// No token for the provider: no request is sent (the hostname would
	// not resolve anyway) and the result reports failure.
	_, ok := sendDataToAuthProviders([]byte(`{}`), map[string]string{"UVA": "uva.invalid"}, map[string]string{}, "sqlDataRequest", "job-1")
	assert.False(t, ok)
}

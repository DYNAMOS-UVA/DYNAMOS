package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDATProvider_Registered(t *testing.T) {
	assert.Contains(t, auth.DefaultRegistry.Names(), datProviderName)
	v, err := auth.DefaultRegistry.Build(datProviderName, nil)
	require.NoError(t, err)
	assert.IsType(t, datVerifier{}, v)
}

func TestDATProvider_ValidToken(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	withFixtureResolver(t, testHolderDID, &priv.PublicKey)

	p, err := datVerifier{}.Verify(context.Background(), datToken(t, jwt.SigningMethodES256, priv, testHolderDID))
	require.NoError(t, err)
	assert.Equal(t, testHolderDID, p.Subject)
	assert.Equal(t, testHolderDID, p.Issuer)
	assert.Equal(t, auth.KindParticipant, p.Kind)
	assert.Equal(t, datProviderName, p.Provider)
}

func TestDATProvider_WrongKey(t *testing.T) {
	signer, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	published, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	withFixtureResolver(t, testHolderDID, &published.PublicKey)

	_, err := datVerifier{}.Verify(context.Background(), datToken(t, jwt.SigningMethodES256, signer, testHolderDID))
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestDATProvider_NonDIDTokensAreUnsupported(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := datVerifier{}.Verify(context.Background(), "opaque")
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)

	_, err = datVerifier{}.Verify(context.Background(), datToken(t, jwt.SigningMethodES256, priv, "policy-enforcer"))
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)
}

func TestDATProvider_WithMiddleware(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	withFixtureResolver(t, testHolderDID, &priv.PublicKey)

	h := auth.Middleware(datVerifier{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		w.Write([]byte(p.Subject))
	}))

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+datToken(t, jwt.SigningMethodES256, priv, testHolderDID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testHolderDID, rec.Body.String())
}

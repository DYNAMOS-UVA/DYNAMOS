package main

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransferServiceClient_SendsServiceToken checks the token that
// transfer-process-service gets: it must pass the same verifier settings
// as the chart gives that service.
func TestTransferServiceClient_SendsServiceToken(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"providerPid":"p","state":"REQUESTED"}`))
	}))
	defer ts.Close()
	defer func(old string) { transferServiceURL = old }(transferServiceURL)
	transferServiceURL = ts.URL

	_, err := fetchTransfer("p")
	require.NoError(t, err)

	token, ok := strings.CutPrefix(got, "Bearer ")
	require.True(t, ok, "Authorization header must be a bearer token")
	v, err := jwtauth.NewVerifier(testInternalIssuerName, []ed25519.PublicKey{testInternalIssuer.PublicKey()}, []string{"transfer-process-service"})
	require.NoError(t, err)
	p, err := v.Verify(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, api.DSPConnectorSubject, p.Subject)
	assert.Equal(t, auth.KindService, p.Kind)
	assert.Equal(t, []string{api.TransferServiceScope}, p.Scopes)
	assert.LessOrEqual(t, p.ExpiresAt.Sub(time.Now()), internalTokenTTL)
}

// TestTransferServiceClient_NoIssuerSendsNothing checks that a connector
// without an issuer fails the call and does not send it unauthenticated.
func TestTransferServiceClient_NoIssuerSendsNothing(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer ts.Close()
	defer func(old string) { transferServiceURL = old }(transferServiceURL)
	transferServiceURL = ts.URL
	defer func(old auth.Issuer) { internalTokenIssuer = old }(internalTokenIssuer)
	internalTokenIssuer = nil

	_, err := postTransfer("/internal/v1/transfers", map[string]string{"consumerPid": "c"})

	assert.ErrorIs(t, err, errNoInternalIssuer)
	assert.Zero(t, calls.Load())
}

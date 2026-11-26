package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testGatewayIssuerName = "dynamos-transfer-process-service"

var testGatewayIssuer *jwtauth.Issuer

// init gives every test in this package (unit and integration builds) a
// working issuer, as a configured deployment would have.
func init() {
	key, err := jwtauth.GenerateKey()
	if err != nil {
		panic(err)
	}
	testGatewayIssuer, err = jwtauth.NewIssuer(testGatewayIssuerName, key)
	if err != nil {
		panic(err)
	}
	gatewayTokenIssuer = testGatewayIssuer
}

func gatewayVerifier(t *testing.T) auth.Verifier {
	t.Helper()
	v, err := jwtauth.NewVerifier(testGatewayIssuerName, []ed25519.PublicKey{testGatewayIssuer.PublicKey()}, []string{gatewayAudience})
	require.NoError(t, err)
	return v
}

func TestRequestJobExecution_SendsParticipantToken(t *testing.T) {
	var authHeader string
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobId":"job-1","responses":[]}`))
	})

	tp := fixtureTransferForJob(json.RawMessage(`{"query":"SELECT 1"}`))
	_, err := requestJobExecution(tp)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(authHeader, "Bearer "))
	p, err := gatewayVerifier(t).Verify(context.Background(), strings.TrimPrefix(authHeader, "Bearer "))
	require.NoError(t, err)
	assert.Equal(t, tp.Participant, p.Subject)
	assert.Equal(t, auth.KindParticipant, p.Kind)
	assert.True(t, p.HasScope(api.GatewayRequestScope))
}

func TestRequestJobExecution_NoIssuerFailsClosed(t *testing.T) {
	saved := gatewayTokenIssuer
	t.Cleanup(func() { gatewayTokenIssuer = saved })
	gatewayTokenIssuer = nil

	called := false
	startFixtureAPIGateway(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	_, err := requestJobExecution(fixtureTransferForJob(json.RawMessage(`{"query":"SELECT 1"}`)))
	assert.ErrorIs(t, err, errNoGatewayIssuer)
	assert.False(t, called, "no unauthenticated request may reach the gateway")
}

func TestGatewayTokenRequiresParticipant(t *testing.T) {
	_, err := gatewayToken(context.Background(), "")
	assert.Error(t, err)
}

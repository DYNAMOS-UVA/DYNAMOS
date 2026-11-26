package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testIssuer(t *testing.T) (*jwtauth.Issuer, func(aud string) *jwtauth.Verifier) {
	t.Helper()
	key, err := jwtauth.GenerateKey()
	require.NoError(t, err)
	iss, err := jwtauth.NewIssuer("dynamos-orchestrator", key)
	require.NoError(t, err)
	return iss, func(aud string) *jwtauth.Verifier {
		v, err := jwtauth.NewVerifier("dynamos-orchestrator", []ed25519.PublicKey{iss.PublicKey()}, []string{aud})
		require.NoError(t, err)
		return v
	}
}

func TestIssueProviderTokens(t *testing.T) {
	iss, verifierFor := testIssuer(t)
	targets := map[string]string{"UVA": "uva.uva.svc", "SURF": "surf.surf.svc"}

	tokens, err := issueProviderTokens(context.Background(), iss, "user@uva.nl", "job-1", "sqlDataRequest", targets)
	require.NoError(t, err)
	require.Len(t, tokens, 2)

	for target := range targets {
		p, err := verifierFor(target).Verify(context.Background(), tokens[target])
		require.NoError(t, err, target)
		assert.Equal(t, "user@uva.nl", p.Subject)
		assert.Equal(t, auth.KindUser, p.Kind)
		assert.True(t, p.HasScope(api.AgentRequestScope("sqlDataRequest")))
		jobID, _ := p.Attribute(api.JobIDAttribute)
		assert.Equal(t, "job-1", jobID)
	}

	// The UVA token must not work at SURF.
	_, err = verifierFor("SURF").Verify(context.Background(), tokens["UVA"])
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestIssueProviderTokensParticipantKind(t *testing.T) {
	iss, verifierFor := testIssuer(t)
	tokens, err := issueProviderTokens(context.Background(), iss, "did:web:consumer", "job-1", "sqlDataRequest", map[string]string{"VU": "vu"})
	require.NoError(t, err)
	p, err := verifierFor("VU").Verify(context.Background(), tokens["VU"])
	require.NoError(t, err)
	assert.Equal(t, auth.KindParticipant, p.Kind)
}

type failingIssuer struct{}

func (failingIssuer) Issue(context.Context, auth.Principal, auth.Grant) (auth.Token, error) {
	return auth.Token{}, errors.New("signing failed")
}

func TestIssueProviderTokensErrors(t *testing.T) {
	ctx := context.Background()
	targets := map[string]string{"UVA": "uva"}
	iss, _ := testIssuer(t)

	_, err := issueProviderTokens(ctx, nil, "u", "j", "sqlDataRequest", targets)
	assert.Error(t, err, "no issuer")
	_, err = issueProviderTokens(ctx, iss, "", "j", "sqlDataRequest", targets)
	assert.Error(t, err, "no user")
	_, err = issueProviderTokens(ctx, iss, "u", "", "sqlDataRequest", targets)
	assert.Error(t, err, "no job")
	_, err = issueProviderTokens(ctx, iss, "u", "j", "", targets)
	assert.Error(t, err, "no request type")
	_, err = issueProviderTokens(ctx, failingIssuer{}, "u", "j", "sqlDataRequest", targets)
	assert.Error(t, err)
}

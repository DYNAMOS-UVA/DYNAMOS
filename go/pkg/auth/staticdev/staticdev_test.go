package staticdev

import (
	"context"
	"errors"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactoryRespectsBuildTag(t *testing.T) {
	v, err := auth.DefaultRegistry.Build(Name, auth.Settings{"token": "t", "subject": "dev"})
	if compiledIn {
		require.NoError(t, err)
		assert.NotNil(t, v)
		return
	}
	assert.ErrorIs(t, err, ErrDisabled)
	assert.Nil(t, v)
}

func TestRegistered(t *testing.T) {
	assert.Contains(t, auth.DefaultRegistry.Names(), Name)
}

func TestNewVerifierSettings(t *testing.T) {
	_, err := newVerifier(auth.Settings{"subject": "dev"})
	assert.ErrorIs(t, err, auth.ErrProviderConfig, "missing token")

	_, err = newVerifier(auth.Settings{"token": "t"})
	assert.ErrorIs(t, err, auth.ErrProviderConfig, "missing subject")

	_, err = newVerifier(auth.Settings{"token": "t", "subject": "dev", "kind": "robot"})
	assert.ErrorIs(t, err, auth.ErrProviderConfig, "unknown kind")
}

func TestVerify(t *testing.T) {
	v, err := newVerifier(auth.Settings{
		"token":   "dev-secret",
		"subject": "dev-user",
		"kind":    "user",
		"scopes":  " orchestrator:read, orchestrator:write ,,",
	})
	require.NoError(t, err)

	p, err := v.Verify(context.Background(), "dev-secret")
	require.NoError(t, err)
	assert.Equal(t, "dev-user", p.Subject)
	assert.Equal(t, auth.KindUser, p.Kind)
	assert.Equal(t, Name, p.Issuer)
	assert.Equal(t, Name, p.Provider)
	assert.Equal(t, []string{"orchestrator:read", "orchestrator:write"}, p.Scopes)

	// The caller must not be able to change the shared principal.
	p.Scopes[0] = "tampered"
	p2, _ := v.Verify(context.Background(), "dev-secret")
	assert.Equal(t, "orchestrator:read", p2.Scopes[0])

	_, err = v.Verify(context.Background(), "wrong")
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)
	assert.NotContains(t, err.Error(), "dev-secret")
}

func TestComposesWithAny(t *testing.T) {
	a, err := newVerifier(auth.Settings{"token": "a", "subject": "svc-a"})
	require.NoError(t, err)
	b, err := newVerifier(auth.Settings{"token": "b", "subject": "svc-b"})
	require.NoError(t, err)
	v := auth.Any(a, b)

	p, err := v.Verify(context.Background(), "b")
	require.NoError(t, err)
	assert.Equal(t, "svc-b", p.Subject)

	_, err = v.Verify(context.Background(), "c")
	assert.True(t, errors.Is(err, auth.ErrInvalidCredentials))
}

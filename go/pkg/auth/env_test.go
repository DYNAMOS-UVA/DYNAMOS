package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvName(t *testing.T) {
	assert.Equal(t, "STATIC_DEV", EnvName("static-dev"))
	assert.Equal(t, "JWT", EnvName("jwt"))
}

func TestSettingsFromEnviron(t *testing.T) {
	env := []string{
		"AUTH_JWT_ISSUER=orchestrator",
		"AUTH_JWT_PUBLIC_KEY_FILES=/a.pem,/b.pem",
		"AUTH_JWT_=ignored",
		"AUTH_JWTX_ISSUER=other provider",
		"OTHER_JWT_ISSUER=other prefix",
		"AUTH_STATIC_DEV_TOKEN=a=b",
		"MALFORMED",
	}
	assert.Equal(t, Settings{"issuer": "orchestrator", "public_key_files": "/a.pem,/b.pem"},
		SettingsFromEnviron("AUTH", "jwt", env))
	assert.Equal(t, Settings{"token": "a=b"}, SettingsFromEnviron("AUTH", "static-dev", env),
		"value keeps '=' characters")
}

func TestSpecsFromEnviron(t *testing.T) {
	_, err := SpecsFromEnviron("AUTH", nil)
	assert.ErrorIs(t, err, ErrProviderConfig)
	_, err = SpecsFromEnviron("AUTH", []string{"AUTH_PROVIDERS= , "})
	assert.ErrorIs(t, err, ErrProviderConfig)
	_, err = SpecsFromEnviron("AUTH", []string{"AUTH_PROVIDERS=jwt,jwt"})
	assert.ErrorIs(t, err, ErrProviderConfig)

	specs, err := SpecsFromEnviron("AUTH", []string{
		"AUTH_PROVIDERS=oidc, jwt",
		"AUTH_OIDC_ISSUER=https://idp",
		"AUTH_JWT_ISSUER=orchestrator",
	})
	require.NoError(t, err)
	assert.Equal(t, []ProviderSpec{
		{Name: "oidc", Settings: Settings{"issuer": "https://idp"}},
		{Name: "jwt", Settings: Settings{"issuer": "orchestrator"}},
	}, specs)
}

func TestIssuerFromEnviron(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.RegisterIssuer("fake", func(s Settings) (Issuer, error) {
		if _, err := s.Require("key"); err != nil {
			return nil, err
		}
		return fakeIssuer{}, nil
	}))

	_, err := issuerFromEnviron(r, "TOKEN_ISSUER", nil)
	assert.ErrorIs(t, err, ErrProviderConfig)
	_, err = issuerFromEnviron(r, "TOKEN_ISSUER", []string{"TOKEN_ISSUER_PROVIDER=fake,other"})
	assert.ErrorIs(t, err, ErrProviderConfig)
	_, err = issuerFromEnviron(r, "TOKEN_ISSUER", []string{"TOKEN_ISSUER_PROVIDER=fake"})
	assert.ErrorIs(t, err, ErrProviderConfig, "missing key setting")

	i, err := issuerFromEnviron(r, "TOKEN_ISSUER", []string{"TOKEN_ISSUER_PROVIDER=fake", "TOKEN_ISSUER_FAKE_KEY=k"})
	require.NoError(t, err)
	tok, err := i.Issue(context.Background(), Principal{}, Grant{})
	require.NoError(t, err)
	assert.Equal(t, "t", tok.Value)
}

func TestFromEnvUseProcessEnvironment(t *testing.T) {
	t.Setenv("ENVTEST_PROVIDERS", "")
	_, err := VerifierFromEnv("ENVTEST")
	assert.ErrorIs(t, err, ErrProviderConfig)

	t.Setenv("ENVTEST_PROVIDERS", "no-such-provider")
	_, err = VerifierFromEnv("ENVTEST")
	assert.ErrorIs(t, err, ErrUnknownProvider)

	t.Setenv("ENVTEST_PROVIDER", "no-such-issuer")
	_, err = IssuerFromEnv("ENVTEST")
	assert.ErrorIs(t, err, ErrUnknownProvider)
}

package jwtauth

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIssuer = "policy-enforcer"

var ctx = context.Background()

type fixture struct {
	key    ed25519.PrivateKey
	issuer *Issuer
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := GenerateKey()
	require.NoError(t, err)
	f := &fixture{key: key, now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	f.issuer, err = NewIssuer(testIssuer, key, WithIssuerClock(func() time.Time { return f.now }))
	require.NoError(t, err)
	return f
}

func (f *fixture) verifier(t *testing.T, audience ...string) *Verifier {
	t.Helper()
	v, err := NewVerifier(testIssuer, []ed25519.PublicKey{f.issuer.PublicKey()}, audience,
		WithVerifierClock(func() time.Time { return f.now }))
	require.NoError(t, err)
	return v
}

func (f *fixture) issue(t *testing.T, grant auth.Grant) auth.Token {
	t.Helper()
	tok, err := f.issuer.Issue(ctx, auth.Principal{Subject: "user@uva.nl", Kind: auth.KindUser}, grant)
	require.NoError(t, err)
	return tok
}

func TestIssueAndVerifyRoundTrip(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{
		Audience:   []string{"UVA"},
		Scopes:     []string{"agent:sqlDataRequest"},
		TTL:        2 * time.Minute,
		Attributes: map[string]string{"jobId": "job-1"},
	})
	assert.Equal(t, f.now.Add(2*time.Minute), tok.ExpiresAt)

	p, err := f.verifier(t, "UVA").Verify(ctx, tok.Value)
	require.NoError(t, err)
	assert.Equal(t, "user@uva.nl", p.Subject)
	assert.Equal(t, testIssuer, p.Issuer)
	assert.Equal(t, auth.KindUser, p.Kind)
	assert.Equal(t, Name, p.Provider)
	assert.Equal(t, []string{"agent:sqlDataRequest"}, p.Scopes)
	assert.Equal(t, []string{"UVA"}, p.Audience)
	assert.True(t, p.ExpiresAt.Equal(tok.ExpiresAt))
	jobID, _ := p.Attribute("jobId")
	assert.Equal(t, "job-1", jobID)
	jti, ok := p.Attribute("jti")
	assert.True(t, ok)
	assert.NotEmpty(t, jti)
}

func TestTokenHeader(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{Audience: []string{"UVA"}})
	parsed, _, err := jwt.NewParser().ParseUnverified(tok.Value, jwt.MapClaims{})
	require.NoError(t, err)
	assert.Equal(t, "EdDSA", parsed.Header["alg"])
	assert.Equal(t, KeyID(f.issuer.PublicKey()), parsed.Header["kid"])
}

func TestAudienceBinding(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{Audience: []string{"UVA"}})

	_, err := f.verifier(t, "VU").Verify(ctx, tok.Value)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials, "token for UVA must not work at VU")

	_, err = f.verifier(t, "VU", "UVA").Verify(ctx, tok.Value)
	assert.NoError(t, err, "any configured audience matches")
}

func TestExpiryAndLeeway(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{Audience: []string{"UVA"}, TTL: time.Minute})
	v := f.verifier(t, "UVA")

	f.now = f.now.Add(time.Minute + 20*time.Second)
	_, err := v.Verify(ctx, tok.Value)
	assert.NoError(t, err, "inside 30s leeway")

	f.now = f.now.Add(20 * time.Second)
	_, err = v.Verify(ctx, tok.Value)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials, "expired")
}

func TestNotYetValid(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{Audience: []string{"UVA"}})
	v := f.verifier(t, "UVA")
	f.now = f.now.Add(-time.Minute)
	_, err := v.Verify(ctx, tok.Value)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestTTLDefaultsAndCap(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{Audience: []string{"UVA"}})
	assert.Equal(t, f.now.Add(DefaultTTL), tok.ExpiresAt)

	tok = f.issue(t, auth.Grant{Audience: []string{"UVA"}, TTL: 48 * time.Hour})
	assert.Equal(t, f.now.Add(DefaultMaxTTL), tok.ExpiresAt)
}

func TestWrongKeyRejected(t *testing.T) {
	f := newFixture(t)
	other := newFixture(t)
	tok := other.issue(t, auth.Grant{Audience: []string{"UVA"}})
	_, err := f.verifier(t, "UVA").Verify(ctx, tok.Value)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestKeyRotation(t *testing.T) {
	oldF := newFixture(t)
	newF := newFixture(t)
	v, err := NewVerifier(testIssuer,
		[]ed25519.PublicKey{oldF.issuer.PublicKey(), newF.issuer.PublicKey()}, []string{"UVA"},
		WithVerifierClock(func() time.Time { return oldF.now }))
	require.NoError(t, err)

	for _, f := range []*fixture{oldF, newF} {
		_, err := v.Verify(ctx, f.issue(t, auth.Grant{Audience: []string{"UVA"}}).Value)
		assert.NoError(t, err)
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	f := newFixture(t)
	tok := f.issue(t, auth.Grant{Audience: []string{"UVA"}})
	parts := strings.Split(tok.Value, ".")
	require.Len(t, parts, 3)

	// Re-sign the same payload with another key but keep the trusted kid.
	other, _ := GenerateKey()
	forged := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": testIssuer, "sub": "admin", "aud": "UVA",
		"exp": f.now.Add(time.Minute).Unix(), "iat": f.now.Unix(),
	})
	forged.Header["kid"] = KeyID(f.issuer.PublicKey())
	forgedValue, err := forged.SignedString(other)
	require.NoError(t, err)
	_, err = f.verifier(t, "UVA").Verify(ctx, forgedValue)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)

	// Change one payload byte.
	parts[1] = parts[1][:len(parts[1])-2] + "AA"
	_, err = f.verifier(t, "UVA").Verify(ctx, strings.Join(parts, "."))
	assert.Error(t, err)
}

func TestAlgorithmConfusionRejected(t *testing.T) {
	f := newFixture(t)
	// HS256 signed with the public key bytes as HMAC secret: classic attack.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": testIssuer, "sub": "admin", "aud": "UVA",
		"exp": f.now.Add(time.Minute).Unix(), "iat": f.now.Unix(),
	})
	tok.Header["kid"] = KeyID(f.issuer.PublicKey())
	signed, err := tok.SignedString([]byte(f.issuer.PublicKey()))
	require.NoError(t, err)
	_, err = f.verifier(t, "UVA").Verify(ctx, signed)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)

	// alg "none".
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"iss": testIssuer, "sub": "admin", "aud": "UVA",
		"exp": f.now.Add(time.Minute).Unix(), "iat": f.now.Unix(),
	})
	unsigned, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	_, err = f.verifier(t, "UVA").Verify(ctx, unsigned)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestMissingClaimsRejected(t *testing.T) {
	f := newFixture(t)
	sign := func(c jwt.MapClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
		tok.Header["kid"] = KeyID(f.issuer.PublicKey())
		s, err := tok.SignedString(f.key)
		require.NoError(t, err)
		return s
	}
	exp := f.now.Add(time.Minute).Unix()
	cases := map[string]jwt.MapClaims{
		"no exp":   {"iss": testIssuer, "sub": "s", "aud": "UVA"},
		"no aud":   {"iss": testIssuer, "sub": "s", "exp": exp},
		"no sub":   {"iss": testIssuer, "aud": "UVA", "exp": exp},
		"bad kind": {"iss": testIssuer, "sub": "s", "aud": "UVA", "exp": exp, "kind": "root"},
		"future iat": {"iss": testIssuer, "sub": "s", "aud": "UVA", "exp": exp,
			"iat": f.now.Add(10 * time.Minute).Unix()},
	}
	v := f.verifier(t, "UVA")
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(ctx, sign(c))
			assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
		})
	}
	t.Run("no kid", func(t *testing.T) {
		tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": testIssuer, "sub": "s", "aud": "UVA", "exp": exp})
		s, _ := tok.SignedString(f.key)
		_, err := v.Verify(ctx, s)
		assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
	})
}

func TestForeignTokensAreUnsupported(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t, "UVA")

	_, err := v.Verify(ctx, "not-a-jwt")
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)

	otherIssuer, err := NewIssuer("someone-else", f.key)
	require.NoError(t, err)
	tok, err := otherIssuer.Issue(ctx, auth.Principal{Subject: "s"}, auth.Grant{Audience: []string{"UVA"}})
	require.NoError(t, err)
	_, err = v.Verify(ctx, tok.Value)
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)
}

func TestIssueValidation(t *testing.T) {
	f := newFixture(t)
	_, err := f.issuer.Issue(ctx, auth.Principal{}, auth.Grant{Audience: []string{"UVA"}})
	assert.Error(t, err, "no subject")
	_, err = f.issuer.Issue(ctx, auth.Principal{Subject: "s"}, auth.Grant{})
	assert.Error(t, err, "no audience")
	_, err = f.issuer.Issue(ctx, auth.Principal{Subject: "s"}, auth.Grant{Audience: []string{" "}})
	assert.Error(t, err, "blank audience")
	_, err = f.issuer.Issue(ctx, auth.Principal{Subject: "s"}, auth.Grant{Audience: []string{"UVA"}, Scopes: []string{"a b"}})
	assert.Error(t, err, "scope with space")

	// Default kind is service.
	tok, err := f.issuer.Issue(ctx, auth.Principal{Subject: "orchestrator"}, auth.Grant{Audience: []string{"UVA"}})
	require.NoError(t, err)
	p, err := f.verifier(t, "UVA").Verify(ctx, tok.Value)
	require.NoError(t, err)
	assert.Equal(t, auth.KindService, p.Kind)
}

func TestConstructorValidation(t *testing.T) {
	key, _ := GenerateKey()
	pub := key.Public().(ed25519.PublicKey)

	_, err := NewIssuer("", key)
	assert.ErrorIs(t, err, auth.ErrProviderConfig)
	_, err = NewIssuer("i", nil)
	assert.ErrorIs(t, err, auth.ErrProviderConfig)
	_, err = NewIssuer("i", key, WithTTL(2*time.Hour, time.Hour))
	assert.ErrorIs(t, err, auth.ErrProviderConfig)

	_, err = NewVerifier("", []ed25519.PublicKey{pub}, []string{"a"})
	assert.ErrorIs(t, err, auth.ErrProviderConfig)
	_, err = NewVerifier("i", nil, []string{"a"})
	assert.ErrorIs(t, err, auth.ErrProviderConfig)
	_, err = NewVerifier("i", []ed25519.PublicKey{pub}, nil)
	assert.ErrorIs(t, err, auth.ErrProviderConfig)
	_, err = NewVerifier("i", []ed25519.PublicKey{{1, 2}}, []string{"a"})
	assert.ErrorIs(t, err, auth.ErrProviderConfig)
}

func TestPEMRoundTrip(t *testing.T) {
	key, _ := GenerateKey()
	privPEM, err := MarshalPrivateKeyPEM(key)
	require.NoError(t, err)
	got, err := ParsePrivateKeyPEM(privPEM)
	require.NoError(t, err)
	assert.True(t, key.Equal(got))

	key2, _ := GenerateKey()
	pub1, _ := MarshalPublicKeyPEM(key.Public().(ed25519.PublicKey))
	pub2, _ := MarshalPublicKeyPEM(key2.Public().(ed25519.PublicKey))
	keys, err := ParsePublicKeysPEM(append(pub1, pub2...))
	require.NoError(t, err)
	assert.Len(t, keys, 2)

	_, err = ParsePublicKeysPEM(privPEM)
	assert.Error(t, err, "private key in public key slot")
	_, err = ParsePublicKeysPEM([]byte("junk"))
	assert.Error(t, err)
	_, err = ParsePublicKeysPEM(append(pub1, []byte("junk")...))
	assert.Error(t, err, "trailing data")
	_, err = ParsePrivateKeyPEM(pub1)
	assert.Error(t, err)
}

func TestFactoriesThroughRegistry(t *testing.T) {
	assert.Contains(t, auth.DefaultRegistry.Names(), Name)
	assert.Contains(t, auth.DefaultRegistry.IssuerNames(), Name)

	key, _ := GenerateKey()
	privPEM, _ := MarshalPrivateKeyPEM(key)
	pubPEM, _ := MarshalPublicKeyPEM(key.Public().(ed25519.PublicKey))
	dir := t.TempDir()
	privFile := filepath.Join(dir, "signing.pem")
	pubFile := filepath.Join(dir, "verify.pem")
	require.NoError(t, os.WriteFile(privFile, privPEM, 0o600))
	require.NoError(t, os.WriteFile(pubFile, pubPEM, 0o644))

	iss, err := auth.DefaultRegistry.BuildIssuer(Name, auth.Settings{
		"issuer": testIssuer, "private_key_file": privFile, "default_ttl": "1m", "max_ttl": "10m",
	})
	require.NoError(t, err)

	ver, err := auth.DefaultRegistry.Build(Name, auth.Settings{
		"issuer": testIssuer, "audience": "UVA, VU", "public_key_files": pubFile, "leeway": "5s",
	})
	require.NoError(t, err)

	tok, err := iss.Issue(ctx, auth.Principal{Subject: "s"}, auth.Grant{Audience: []string{"VU"}})
	require.NoError(t, err)
	p, err := ver.Verify(ctx, tok.Value)
	require.NoError(t, err)
	assert.Equal(t, "s", p.Subject)

	// Inline PEM works too.
	_, err = auth.DefaultRegistry.Build(Name, auth.Settings{
		"issuer": testIssuer, "audience": "UVA", "public_keys_pem": string(pubPEM),
	})
	assert.NoError(t, err)
}

func TestFactoryErrors(t *testing.T) {
	key, _ := GenerateKey()
	privPEM, _ := MarshalPrivateKeyPEM(key)
	pubPEM, _ := MarshalPublicKeyPEM(key.Public().(ed25519.PublicKey))

	issuerCases := map[string]auth.Settings{
		"no issuer":   {"private_key_pem": string(privPEM)},
		"no key":      {"issuer": "i"},
		"both keys":   {"issuer": "i", "private_key_pem": string(privPEM), "private_key_file": "/x"},
		"bad file":    {"issuer": "i", "private_key_file": "/does/not/exist"},
		"public key":  {"issuer": "i", "private_key_pem": string(pubPEM)},
		"bad ttl":     {"issuer": "i", "private_key_pem": string(privPEM), "default_ttl": "soon"},
		"bad max ttl": {"issuer": "i", "private_key_pem": string(privPEM), "max_ttl": "-1m"},
	}
	for name, s := range issuerCases {
		t.Run("issuer/"+name, func(t *testing.T) {
			_, err := IssuerFactory(s)
			assert.ErrorIs(t, err, auth.ErrProviderConfig)
		})
	}

	verifierCases := map[string]auth.Settings{
		"no issuer":   {"audience": "UVA", "public_keys_pem": string(pubPEM)},
		"no audience": {"issuer": "i", "public_keys_pem": string(pubPEM)},
		"no keys":     {"issuer": "i", "audience": "UVA"},
		"private key": {"issuer": "i", "audience": "UVA", "public_keys_pem": string(privPEM)},
		"bad leeway":  {"issuer": "i", "audience": "UVA", "public_keys_pem": string(pubPEM), "leeway": "x"},
	}
	for name, s := range verifierCases {
		t.Run("verifier/"+name, func(t *testing.T) {
			_, err := VerifierFactory(s)
			assert.ErrorIs(t, err, auth.ErrProviderConfig)
		})
	}
}

func TestComposesWithAny(t *testing.T) {
	a := newFixture(t)
	bKey, _ := GenerateKey()
	bIssuer, _ := NewIssuer("orchestrator-admin", bKey, WithIssuerClock(func() time.Time { return a.now }))
	bVerifier, _ := NewVerifier("orchestrator-admin", []ed25519.PublicKey{bIssuer.PublicKey()}, []string{"UVA"},
		WithVerifierClock(func() time.Time { return a.now }))

	v := auth.Any(a.verifier(t, "UVA"), bVerifier)
	tok, err := bIssuer.Issue(ctx, auth.Principal{Subject: "admin"}, auth.Grant{Audience: []string{"UVA"}})
	require.NoError(t, err)
	p, err := v.Verify(ctx, tok.Value)
	require.NoError(t, err)
	assert.Equal(t, "admin", p.Subject)
}

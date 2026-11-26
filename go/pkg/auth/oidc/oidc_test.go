package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/internal/jwk"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAudience = "api://dynamos"

var ctx = context.Background()

// idp is a fake OIDC provider over TLS.
type idp struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	keys      map[string]*rsa.PrivateKey
	down      atomic.Bool
	jwksHits  atomic.Int32
	docIssuer string // overrides the discovery "issuer" when set
	now       time.Time
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	p := &idp{t: t, keys: map[string]*rsa.PrivateKey{}, now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		iss := p.issuer()
		if p.docIssuer != "" {
			iss = p.docIssuer
		}
		json.NewEncoder(w).Encode(map[string]string{"issuer": iss, "jwks_uri": p.srv.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		p.jwksHits.Add(1)
		if p.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		var set jwk.Set
		for kid, k := range p.keys {
			set.Keys = append(set.Keys, jwk.Key{
				Kty: "RSA", Kid: kid, Use: "sig",
				N: base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
				E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
			})
		}
		json.NewEncoder(w).Encode(set)
	})
	p.srv = httptest.NewTLSServer(mux)
	t.Cleanup(p.srv.Close)
	p.addKey("k1")
	return p
}

func (p *idp) issuer() string { return p.srv.URL }

func (p *idp) addKey(kid string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(p.t, err)
	p.mu.Lock()
	p.keys[kid] = k
	p.mu.Unlock()
}

func (p *idp) baseClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": p.issuer(),
		"aud": testAudience,
		"sub": "user-123",
		"exp": p.now.Add(time.Hour).Unix(),
		"iat": p.now.Unix(),
	}
}

func (p *idp) sign(kid string, c jwt.MapClaims) string {
	p.mu.Lock()
	k := p.keys[kid]
	p.mu.Unlock()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	if k == nil {
		for _, key := range p.keys {
			k = key
		}
	}
	s, err := tok.SignedString(k)
	require.NoError(p.t, err)
	return s
}

func (p *idp) verifier(t *testing.T, mod func(*Config)) *Verifier {
	t.Helper()
	cfg := Config{
		Issuer:     p.issuer(),
		Audience:   []string{testAudience},
		HTTPClient: p.srv.Client(),
		Now:        func() time.Time { return p.now },
	}
	if mod != nil {
		mod(&cfg)
	}
	v, err := New(cfg)
	require.NoError(t, err)
	return v
}

func TestVerifyWithDiscovery(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, func(c *Config) { c.AttributeClaims = []string{"email", "tid", "missing"} })

	claims := p.baseClaims()
	claims["scp"] = "Data.Read Data.Write"
	claims["roles"] = []string{"Steward.Admin"}
	claims["email"] = "user@uva.nl"
	claims["tid"] = 42 // not a string: skipped

	pr, err := v.Verify(ctx, p.sign("k1", claims))
	require.NoError(t, err)
	assert.Equal(t, "user-123", pr.Subject)
	assert.Equal(t, p.issuer(), pr.Issuer)
	assert.Equal(t, auth.KindUser, pr.Kind)
	assert.Equal(t, Name, pr.Provider)
	assert.Equal(t, []string{"Data.Read", "Data.Write", "Steward.Admin"}, pr.Scopes)
	assert.Equal(t, []string{testAudience}, pr.Audience)
	assert.Equal(t, map[string]string{"email": "user@uva.nl"}, pr.Attributes)
	assert.Equal(t, p.now.Add(time.Hour).Unix(), pr.ExpiresAt.Unix())
}

func TestSubjectClaim(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, func(c *Config) { c.SubjectClaim = "oid" })
	c := p.baseClaims()
	_, err := v.Verify(ctx, p.sign("k1", c))
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials, "oid missing")

	c["oid"] = "object-id"
	pr, err := v.Verify(ctx, p.sign("k1", c))
	require.NoError(t, err)
	assert.Equal(t, "object-id", pr.Subject)
}

func TestExplicitJWKSURL(t *testing.T) {
	p := newIDP(t)
	p.docIssuer = "https://evil.example" // discovery would fail
	v := p.verifier(t, func(c *Config) { c.JWKSURL = p.srv.URL + "/keys" })
	_, err := v.Verify(ctx, p.sign("k1", p.baseClaims()))
	assert.NoError(t, err)
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	p := newIDP(t)
	p.docIssuer = "https://evil.example"
	_, err := p.verifier(t, nil).Verify(ctx, p.sign("k1", p.baseClaims()))
	assert.ErrorIs(t, err, auth.ErrUnavailable)
}

func TestRejections(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	cases := map[string]func(jwt.MapClaims){
		"wrong audience": func(c jwt.MapClaims) { c["aud"] = "api://other" },
		"no audience":    func(c jwt.MapClaims) { delete(c, "aud") },
		"expired":        func(c jwt.MapClaims) { c["exp"] = p.now.Add(-2 * time.Minute).Unix() },
		"no exp":         func(c jwt.MapClaims) { delete(c, "exp") },
		"future nbf":     func(c jwt.MapClaims) { c["nbf"] = p.now.Add(5 * time.Minute).Unix() },
		"no sub":         func(c jwt.MapClaims) { delete(c, "sub") },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			c := p.baseClaims()
			mod(c)
			_, err := v.Verify(ctx, p.sign("k1", c))
			assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
		})
	}
}

func TestLeeway(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	c := p.baseClaims()
	c["exp"] = p.now.Add(-30 * time.Second).Unix()
	_, err := v.Verify(ctx, p.sign("k1", c))
	assert.NoError(t, err, "inside default 60s leeway")
}

func TestWrongAlgorithmRejected(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, p.baseClaims())
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString([]byte("secret"))
	require.NoError(t, err)
	_, err = v.Verify(ctx, s)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)

	// Algorithm allowed by config but key type mismatch is still rejected.
	v2 := p.verifier(t, func(c *Config) { c.Algorithms = []string{"ES256"} })
	_, err = v2.Verify(ctx, p.sign("k1", p.baseClaims()))
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestForeignTokensAreUnsupported(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	_, err := v.Verify(ctx, "opaque-token")
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)

	c := p.baseClaims()
	c["iss"] = "https://other-idp.example"
	_, err = v.Verify(ctx, p.sign("k1", c))
	assert.ErrorIs(t, err, auth.ErrUnsupportedToken)
	assert.Zero(t, p.jwksHits.Load(), "foreign tokens must not trigger key fetches")
}

func TestIDPDownIsUnavailable(t *testing.T) {
	p := newIDP(t)
	p.down.Store(true)
	_, err := p.verifier(t, nil).Verify(ctx, p.sign("k1", p.baseClaims()))
	assert.ErrorIs(t, err, auth.ErrUnavailable)
}

func TestStaleCacheServedWhenIDPDown(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	tok := p.sign("k1", p.baseClaims())
	_, err := v.Verify(ctx, tok)
	require.NoError(t, err)

	p.down.Store(true)
	p.now = p.now.Add(DefaultCacheTTL + time.Minute)
	c := p.baseClaims()
	_, err = v.Verify(ctx, p.sign("k1", c))
	assert.NoError(t, err, "known key keeps working during an IdP outage")
}

func TestKeyRotationAndRefreshLimit(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	_, err := v.Verify(ctx, p.sign("k1", p.baseClaims()))
	require.NoError(t, err)
	assert.EqualValues(t, 1, p.jwksHits.Load())

	// Cached and fresh: no new fetch.
	_, err = v.Verify(ctx, p.sign("k1", p.baseClaims()))
	require.NoError(t, err)
	assert.EqualValues(t, 1, p.jwksHits.Load())

	// New key inside MinRefresh: not fetched, rejected.
	p.addKey("k2")
	_, err = v.Verify(ctx, p.sign("k2", p.baseClaims()))
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
	assert.EqualValues(t, 1, p.jwksHits.Load(), "refresh is rate limited")

	// After MinRefresh: fetched, accepted.
	p.now = p.now.Add(DefaultMinRefresh)
	_, err = v.Verify(ctx, p.sign("k2", p.baseClaims()))
	assert.NoError(t, err)
	assert.EqualValues(t, 2, p.jwksHits.Load())

	// Unknown kid after refresh: invalid, not unavailable.
	p.now = p.now.Add(DefaultMinRefresh)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, p.baseClaims())
	tok.Header["kid"] = "ghost"
	s, _ := tok.SignedString(p.keys["k1"])
	_, err = v.Verify(ctx, s)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestTokenWithoutKid(t *testing.T) {
	p := newIDP(t)
	v := p.verifier(t, nil)
	_, err := v.Verify(ctx, p.sign("", p.baseClaims()))
	assert.NoError(t, err, "single key in set")

	p.addKey("k2")
	v2 := p.verifier(t, nil)
	_, err = v2.Verify(ctx, p.sign("", p.baseClaims()))
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials, "ambiguous with two keys")
}

func TestNewValidation(t *testing.T) {
	cases := map[string]Config{
		"http issuer":   {Issuer: "http://idp.example", Audience: []string{"a"}},
		"no issuer":     {Audience: []string{"a"}},
		"no audience":   {Issuer: "https://idp.example"},
		"http jwks":     {Issuer: "https://idp.example", Audience: []string{"a"}, JWKSURL: "http://idp.example/keys"},
		"bad algorithm": {Issuer: "https://idp.example", Audience: []string{"a"}, Algorithms: []string{"HS256"}},
		"none alg":      {Issuer: "https://idp.example", Audience: []string{"a"}, Algorithms: []string{"none"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(c)
			assert.ErrorIs(t, err, auth.ErrProviderConfig)
		})
	}
}

func TestFactory(t *testing.T) {
	assert.Contains(t, auth.DefaultRegistry.Names(), Name)

	v, err := auth.DefaultRegistry.Build(Name, auth.Settings{
		"issuer":           "https://login.microsoftonline.com/tenant/v2.0",
		"audience":         "api://dynamos, client-id",
		"subject_claim":    "oid",
		"scope_claims":     "scp",
		"attribute_claims": "email",
		"algorithms":       "RS256",
		"leeway":           "10s",
		"cache_ttl":        "5m",
		"min_refresh":      "1m",
		"http_timeout":     "3s",
	})
	require.NoError(t, err)
	ov := v.(*Verifier)
	assert.Equal(t, []string{"api://dynamos", "client-id"}, ov.cfg.Audience)
	assert.Equal(t, "oid", ov.cfg.SubjectClaim)
	assert.Equal(t, 10*time.Second, ov.cfg.Leeway)
	assert.Equal(t, 3*time.Second, ov.cfg.HTTPClient.Timeout)

	bad := []auth.Settings{
		{"audience": "a"},
		{"issuer": "https://i", "audience": "a", "leeway": "x"},
		{"issuer": "https://i", "audience": "a", "http_timeout": "x"},
		{"issuer": "https://i"},
	}
	for _, s := range bad {
		_, err := Factory(s)
		assert.ErrorIs(t, err, auth.ErrProviderConfig)
	}
}

func TestComposesWithAny(t *testing.T) {
	a := newIDP(t)
	b := newIDP(t)
	v := auth.Any(a.verifier(t, nil), b.verifier(t, nil))
	pr, err := v.Verify(ctx, b.sign("k1", b.baseClaims()))
	require.NoError(t, err)
	assert.Equal(t, b.issuer(), pr.Issuer)
}

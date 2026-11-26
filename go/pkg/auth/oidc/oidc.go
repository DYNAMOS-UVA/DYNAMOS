// Package oidc is the auth provider for end-user access tokens issued by
// an OpenID Connect identity provider (Microsoft Entra ID, Keycloak, ...).
//
// It verifies JWT access tokens locally against the provider's JWKS. The
// JWKS URL comes from configuration or from OIDC discovery
// ({issuer}/.well-known/openid-configuration). Keys are cached and are
// fetched again when the cache is stale or a token names an unknown key
// ID (key rotation), with a minimum delay between fetches.
//
// Only one issuer per Verifier. To accept several IdPs, build one Verifier
// for each and combine them with auth.Any.
//
// Settings (registry name "oidc"):
//
//	issuer            required. Exact expected "iss" (https URL). For
//	                  Entra v2 tokens: https://login.microsoftonline.com/{tenant}/v2.0
//	audience          required. Comma-separated; the token must name one.
//	                  For Entra: the API's client ID or App ID URI.
//	jwks_url          optional. Skips discovery when set (https URL).
//	subject_claim     optional. Claim used as Principal.Subject. Default "sub".
//	                  Entra: "oid" is stable per user across apps.
//	scope_claims      optional. Comma-separated claims read as scopes.
//	                  Space-separated strings and string arrays both work.
//	                  Default "scp,scope,roles".
//	attribute_claims  optional. Comma-separated string claims copied to
//	                  Principal.Attributes (for example "email,name,tid").
//	algorithms        optional. Default "RS256,ES256".
//	leeway            optional. Default 60s.
//	cache_ttl         optional. JWKS cache lifetime. Default 15m.
//	min_refresh       optional. Minimum delay between JWKS fetches. Default 30s.
//	http_timeout      optional. Default 10s.
package oidc

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/internal/jwk"
	"github.com/golang-jwt/jwt/v5"
)

// Name is the registry name of this provider.
const Name = "oidc"

// Defaults for Config.
const (
	DefaultLeeway     = 60 * time.Second
	DefaultCacheTTL   = 15 * time.Minute
	DefaultMinRefresh = 30 * time.Second
	DefaultTimeout    = 10 * time.Second
	maxDocumentBytes  = 1 << 20
)

var (
	defaultAlgorithms  = []string{"RS256", "ES256"}
	defaultScopeClaims = []string{"scp", "scope", "roles"}
	allowedAlgorithms  = map[string]bool{
		"RS256": true, "RS384": true, "RS512": true,
		"PS256": true, "PS384": true, "PS512": true,
		"ES256": true, "ES384": true, "ES512": true,
		"EdDSA": true,
	}
)

func init() {
	auth.Register(Name, Factory)
}

// Config configures a Verifier. Zero values take the defaults above.
type Config struct {
	Issuer          string
	Audience        []string
	JWKSURL         string
	SubjectClaim    string
	ScopeClaims     []string
	AttributeClaims []string
	Algorithms      []string
	Leeway          time.Duration
	CacheTTL        time.Duration
	MinRefresh      time.Duration
	// HTTPClient fetches discovery and JWKS documents. Default: a client
	// with a 10s timeout.
	HTTPClient *http.Client
	// Now replaces time.Now, for tests.
	Now func() time.Time
}

// Verifier checks OIDC access tokens from one issuer.
type Verifier struct {
	cfg Config

	mu          sync.Mutex
	jwksURL     string
	keys        map[string]crypto.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// New validates cfg and returns a Verifier. It does no network call: the
// first Verify fetches the keys, so a service can start while the IdP is
// unreachable and recover when it comes back.
func New(cfg Config) (*Verifier, error) {
	if err := requireHTTPS("issuer", cfg.Issuer); err != nil {
		return nil, err
	}
	if len(cfg.Audience) == 0 {
		return nil, fmt.Errorf("%w: at least one audience is required", auth.ErrProviderConfig)
	}
	if cfg.JWKSURL != "" {
		if err := requireHTTPS("jwks_url", cfg.JWKSURL); err != nil {
			return nil, err
		}
	}
	if cfg.SubjectClaim == "" {
		cfg.SubjectClaim = "sub"
	}
	if len(cfg.ScopeClaims) == 0 {
		cfg.ScopeClaims = defaultScopeClaims
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = defaultAlgorithms
	}
	for _, a := range cfg.Algorithms {
		if !allowedAlgorithms[a] {
			return nil, fmt.Errorf("%w: algorithm %q is not allowed", auth.ErrProviderConfig, a)
		}
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = DefaultLeeway
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.MinRefresh == 0 {
		cfg.MinRefresh = DefaultMinRefresh
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Audience = append([]string(nil), cfg.Audience...)
	return &Verifier{cfg: cfg, jwksURL: cfg.JWKSURL}, nil
}

func requireHTTPS(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: %s must be an https URL", auth.ErrProviderConfig, name)
	}
	return nil
}

// Verify checks the token signature against the IdP keys, then issuer,
// audience, expiry, not-before and issue time. A token that is not a JWT,
// or comes from another issuer, answers auth.ErrUnsupportedToken. An
// unreachable IdP with no usable cached key answers auth.ErrUnavailable.
func (v *Verifier) Verify(ctx context.Context, token string) (auth.Principal, error) {
	// Routing step only: nothing from this unverified parse is trusted.
	peek := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, peek); err != nil {
		return auth.Principal{}, fmt.Errorf("%w: not a JWT", auth.ErrUnsupportedToken)
	}
	if iss, _ := peek["iss"].(string); iss != v.cfg.Issuer {
		return auth.Principal{}, fmt.Errorf("%w: issuer is not %q", auth.ErrUnsupportedToken, v.cfg.Issuer)
	}

	var keyErr error
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		kid, _ := t.Header["kid"].(string)
		k, err := v.key(ctx, kid)
		keyErr = err
		return k, err
	},
		jwt.WithValidMethods(v.cfg.Algorithms),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.cfg.Audience...),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.cfg.Leeway),
		jwt.WithTimeFunc(v.cfg.Now),
	)
	if errors.Is(keyErr, auth.ErrUnavailable) {
		return auth.Principal{}, keyErr
	}
	if err != nil || !parsed.Valid {
		return auth.Principal{}, fmt.Errorf("%w: %v", auth.ErrInvalidCredentials, err)
	}
	return v.principal(claims)
}

func (v *Verifier) principal(c jwt.MapClaims) (auth.Principal, error) {
	subject, _ := c[v.cfg.SubjectClaim].(string)
	if strings.TrimSpace(subject) == "" {
		return auth.Principal{}, fmt.Errorf("%w: missing %q claim", auth.ErrInvalidCredentials, v.cfg.SubjectClaim)
	}
	aud, _ := c.GetAudience()
	exp, _ := c.GetExpirationTime()

	var scopes []string
	for _, name := range v.cfg.ScopeClaims {
		scopes = append(scopes, stringsClaim(c[name])...)
	}
	var attrs map[string]string
	for _, name := range v.cfg.AttributeClaims {
		if s, ok := c[name].(string); ok {
			if attrs == nil {
				attrs = make(map[string]string)
			}
			attrs[name] = s
		}
	}
	p := auth.Principal{
		Subject:    subject,
		Issuer:     v.cfg.Issuer,
		Kind:       auth.KindUser,
		Provider:   Name,
		Scopes:     scopes,
		Audience:   append([]string(nil), aud...),
		Attributes: attrs,
	}
	if exp != nil {
		p.ExpiresAt = exp.Time
	}
	return p, nil
}

// stringsClaim reads a space-separated string or an array of strings.
func stringsClaim(v interface{}) []string {
	switch t := v.(type) {
	case string:
		return strings.Fields(t)
	case []interface{}:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// key returns the public key for kid. It refreshes the key set when the
// cache is stale or kid is unknown, at most once per MinRefresh. When a
// refresh fails it keeps serving a cached key, so a short IdP outage does
// not reject valid tokens.
func (v *Verifier) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := v.cfg.Now()
	k, found := v.lookup(kid)
	fresh := v.keys != nil && now.Sub(v.fetchedAt) < v.cfg.CacheTTL
	if found && fresh {
		return k, nil
	}

	var fetchErr error
	if v.lastAttempt.IsZero() || now.Sub(v.lastAttempt) >= v.cfg.MinRefresh {
		v.lastAttempt = now
		keys, err := v.fetchKeys(ctx)
		if err == nil {
			v.keys = keys
			v.fetchedAt = now
		} else {
			fetchErr = err
		}
		k, found = v.lookup(kid)
	}
	switch {
	case found:
		return k, nil
	case fetchErr != nil || v.keys == nil:
		if fetchErr == nil {
			fetchErr = errors.New("keys not fetched yet")
		}
		return nil, fmt.Errorf("%w: %v", auth.ErrUnavailable, fetchErr)
	default:
		return nil, errors.New("unknown kid")
	}
}

// lookup finds kid in the cached set. A token without kid matches only
// when the set holds exactly one key.
func (v *Verifier) lookup(kid string) (crypto.PublicKey, bool) {
	if v.keys == nil {
		return nil, false
	}
	if kid == "" && len(v.keys) == 1 {
		for _, k := range v.keys {
			return k, true
		}
	}
	k, ok := v.keys[kid]
	return k, ok
}

func (v *Verifier) fetchKeys(ctx context.Context) (map[string]crypto.PublicKey, error) {
	if v.jwksURL == "" {
		u, err := v.discover(ctx)
		if err != nil {
			return nil, err
		}
		v.jwksURL = u
	}
	body, err := v.get(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("fetching JWKS: %w", err)
	}
	return jwk.ParseSet(body)
}

func (v *Verifier) discover(ctx context.Context) (string, error) {
	u := strings.TrimSuffix(v.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	body, err := v.get(ctx, u)
	if err != nil {
		return "", fmt.Errorf("fetching discovery document: %w", err)
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("parsing discovery document: %w", err)
	}
	// OIDC Discovery 1.0 section 4.3: the issuer must match exactly.
	if doc.Issuer != v.cfg.Issuer {
		return "", fmt.Errorf("discovery issuer %q does not match configured issuer", doc.Issuer)
	}
	if err := requireHTTPS("jwks_uri", doc.JWKSURI); err != nil {
		return "", err
	}
	return doc.JWKSURI, nil
}

func (v *Verifier) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))
}

// Factory builds a Verifier from settings (see the package doc).
func Factory(s auth.Settings) (auth.Verifier, error) {
	issuer, err := s.Require("issuer")
	if err != nil {
		return nil, err
	}
	cfg := Config{
		Issuer:          issuer,
		Audience:        s.List("audience"),
		JWKSURL:         strings.TrimSpace(s.Get("jwks_url")),
		SubjectClaim:    strings.TrimSpace(s.Get("subject_claim")),
		ScopeClaims:     s.List("scope_claims"),
		AttributeClaims: s.List("attribute_claims"),
		Algorithms:      s.List("algorithms"),
	}
	durations := []struct {
		key string
		def time.Duration
		dst *time.Duration
	}{
		{"leeway", DefaultLeeway, &cfg.Leeway},
		{"cache_ttl", DefaultCacheTTL, &cfg.CacheTTL},
		{"min_refresh", DefaultMinRefresh, &cfg.MinRefresh},
	}
	for _, d := range durations {
		if *d.dst, err = s.Duration(d.key, d.def); err != nil {
			return nil, err
		}
	}
	timeout, err := s.Duration("http_timeout", DefaultTimeout)
	if err != nil {
		return nil, err
	}
	cfg.HTTPClient = &http.Client{Timeout: timeout}
	return New(cfg)
}

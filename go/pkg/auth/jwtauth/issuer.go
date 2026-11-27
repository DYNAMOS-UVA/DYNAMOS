package jwtauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Issuer mints Ed25519-signed service tokens.
type Issuer struct {
	issuer     string
	key        ed25519.PrivateKey
	kid        string
	defaultTTL time.Duration
	maxTTL     time.Duration
	now        func() time.Time
}

// IssuerOption configures NewIssuer.
type IssuerOption func(*Issuer)

// WithTTL sets the lifetime used when a Grant gives none (def) and the cap
// applied to every Grant (max). Zero keeps the current value.
func WithTTL(def, max time.Duration) IssuerOption {
	return func(i *Issuer) {
		if def > 0 {
			i.defaultTTL = def
		}
		if max > 0 {
			i.maxTTL = max
		}
	}
}

// WithIssuerClock replaces time.Now, for tests.
func WithIssuerClock(now func() time.Time) IssuerOption {
	return func(i *Issuer) { i.now = now }
}

// NewIssuer returns an Issuer that signs as issuer with key.
func NewIssuer(issuer string, key ed25519.PrivateKey, opts ...IssuerOption) (*Issuer, error) {
	if err := validIssuerName(issuer); err != nil {
		return nil, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: Ed25519 private key is required", auth.ErrProviderConfig)
	}
	i := &Issuer{
		issuer:     issuer,
		key:        key,
		kid:        KeyID(key.Public().(ed25519.PublicKey)),
		defaultTTL: DefaultTTL,
		maxTTL:     DefaultMaxTTL,
		now:        time.Now,
	}
	for _, o := range opts {
		o(i)
	}
	if i.defaultTTL > i.maxTTL {
		return nil, fmt.Errorf("%w: default TTL %s is above max TTL %s", auth.ErrProviderConfig, i.defaultTTL, i.maxTTL)
	}
	return i, nil
}

// PublicKey returns the key verifiers must trust.
func (i *Issuer) PublicKey() ed25519.PublicKey {
	return i.key.Public().(ed25519.PublicKey)
}

// Issue mints a token for subject. subject.Subject and grant.Audience are
// required: a token without a bound recipient could be replayed against
// any service. grant.TTL is capped at the max TTL.
func (i *Issuer) Issue(_ context.Context, subject auth.Principal, grant auth.Grant) (auth.Token, error) {
	if strings.TrimSpace(subject.Subject) == "" {
		return auth.Token{}, errors.New("jwtauth: subject is required")
	}
	if len(grant.Audience) == 0 {
		return auth.Token{}, errors.New("jwtauth: at least one audience is required")
	}
	for _, a := range grant.Audience {
		if strings.TrimSpace(a) == "" {
			return auth.Token{}, errors.New("jwtauth: audience entries must not be empty")
		}
	}
	scope, err := scopeString(grant.Scopes)
	if err != nil {
		return auth.Token{}, err
	}

	ttl := grant.TTL
	if ttl <= 0 {
		ttl = i.defaultTTL
	}
	if ttl > i.maxTTL {
		ttl = i.maxTTL
	}
	kind := subject.Kind
	if kind == "" {
		kind = auth.KindService
	}

	// JWT NumericDate has one-second resolution. Truncate so ExpiresAt
	// matches the exp claim exactly.
	now := i.now().Truncate(time.Second)
	exp := now.Add(ttl)
	c := claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			Subject:   subject.Subject,
			Audience:  jwt.ClaimStrings(append([]string(nil), grant.Audience...)),
			ExpiresAt: jwt.NewNumericDate(exp),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.NewString(),
		},
		Kind:  kind,
		Scope: scope,
		Attrs: cloneAttrs(grant.Attributes),
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = i.kid
	signed, err := tok.SignedString(i.key)
	if err != nil {
		return auth.Token{}, fmt.Errorf("jwtauth: signing token: %w", err)
	}
	return auth.Token{Value: signed, ExpiresAt: exp}, nil
}

// IssuerFactory builds an Issuer from settings:
//
//	issuer            required. Issuer name written to "iss".
//	private_key_file  path to a PKCS #8 Ed25519 PEM file (for example a
//	                  mounted Kubernetes Secret), or
//	private_key_pem   the PEM itself. Exactly one of the two is required.
//	default_ttl       optional. Default 5m.
//	max_ttl           optional. Default 1h.
func IssuerFactory(s auth.Settings) (auth.Issuer, error) {
	issuer, err := s.Require("issuer")
	if err != nil {
		return nil, err
	}
	pemData, err := keyMaterial(s, "private_key_file", "private_key_pem")
	if err != nil {
		return nil, err
	}
	key, err := ParsePrivateKeyPEM(pemData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrProviderConfig, err)
	}
	def, err := s.Duration("default_ttl", DefaultTTL)
	if err != nil {
		return nil, err
	}
	max, err := s.Duration("max_ttl", DefaultMaxTTL)
	if err != nil {
		return nil, err
	}
	return NewIssuer(issuer, key, WithTTL(def, max))
}

// keyMaterial returns the PEM from exactly one of the file setting or the
// inline setting.
func keyMaterial(s auth.Settings, fileKey, inlineKey string) ([]byte, error) {
	files := s.List(fileKey)
	inline := strings.TrimSpace(s.Get(inlineKey))
	switch {
	case len(files) > 0 && inline != "":
		return nil, fmt.Errorf("%w: set %q or %q, not both", auth.ErrProviderConfig, fileKey, inlineKey)
	case len(files) > 0:
		b, err := readFiles(files)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrProviderConfig, err)
		}
		return b, nil
	case inline != "":
		return []byte(inline), nil
	default:
		return nil, fmt.Errorf("%w: %q or %q is required", auth.ErrProviderConfig, fileKey, inlineKey)
	}
}

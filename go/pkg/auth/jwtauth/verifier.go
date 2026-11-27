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
)

// Verifier checks Ed25519 service tokens from one issuer.
type Verifier struct {
	issuer   string
	keys     map[string]ed25519.PublicKey
	audience []string
	leeway   time.Duration
	now      func() time.Time
}

// VerifierOption configures NewVerifier.
type VerifierOption func(*Verifier)

// WithLeeway sets the allowed clock difference. Default 30s.
func WithLeeway(d time.Duration) VerifierOption {
	return func(v *Verifier) { v.leeway = d }
}

// WithVerifierClock replaces time.Now, for tests.
func WithVerifierClock(now func() time.Time) VerifierOption {
	return func(v *Verifier) { v.now = now }
}

// NewVerifier returns a Verifier for tokens from issuer, signed by one of
// keys, and addressed to at least one of audience. audience is required:
// a service accepts only tokens minted for it.
func NewVerifier(issuer string, keys []ed25519.PublicKey, audience []string, opts ...VerifierOption) (*Verifier, error) {
	if err := validIssuerName(issuer); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: at least one public key is required", auth.ErrProviderConfig)
	}
	if len(audience) == 0 {
		return nil, fmt.Errorf("%w: at least one audience is required", auth.ErrProviderConfig)
	}
	v := &Verifier{
		issuer:   issuer,
		keys:     make(map[string]ed25519.PublicKey, len(keys)),
		audience: append([]string(nil), audience...),
		leeway:   DefaultLeeway,
		now:      time.Now,
	}
	for _, k := range keys {
		if len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: bad Ed25519 public key", auth.ErrProviderConfig)
		}
		v.keys[KeyID(k)] = k
	}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

// Verify checks signature, issuer, audience, expiry, not-before and issue
// time. A token that is not a JWT, or comes from another issuer, answers
// auth.ErrUnsupportedToken so auth.Any can hand it to another provider.
func (v *Verifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	// Routing step only: nothing from this unverified parse is trusted.
	var peek claims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &peek); err != nil {
		return auth.Principal{}, fmt.Errorf("%w: not a JWT", auth.ErrUnsupportedToken)
	}
	if peek.Issuer != v.issuer {
		return auth.Principal{}, fmt.Errorf("%w: issuer is not %q", auth.ErrUnsupportedToken, v.issuer)
	}

	var c claims
	parsed, err := jwt.ParseWithClaims(token, &c, v.keyFunc,
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience...),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.leeway),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil || !parsed.Valid {
		return auth.Principal{}, fmt.Errorf("%w: %v", auth.ErrInvalidCredentials, err)
	}
	if strings.TrimSpace(c.Subject) == "" {
		return auth.Principal{}, fmt.Errorf("%w: missing sub claim", auth.ErrInvalidCredentials)
	}
	kind := c.Kind
	switch kind {
	case "":
		kind = auth.KindService
	case auth.KindUser, auth.KindService, auth.KindParticipant:
	default:
		return auth.Principal{}, fmt.Errorf("%w: unknown kind claim", auth.ErrInvalidCredentials)
	}

	attrs := cloneAttrs(c.Attrs)
	if c.ID != "" {
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs["jti"] = c.ID
	}
	return auth.Principal{
		Subject:    c.Subject,
		Issuer:     c.Issuer,
		Kind:       kind,
		Provider:   Name,
		Scopes:     strings.Fields(c.Scope),
		Audience:   append([]string(nil), c.Audience...),
		ExpiresAt:  c.ExpiresAt.Time,
		Attributes: attrs,
	}, nil
}

func (v *Verifier) keyFunc(t *jwt.Token) (interface{}, error) {
	kid, _ := t.Header["kid"].(string)
	if kid == "" {
		return nil, errors.New("missing kid header")
	}
	k, ok := v.keys[kid]
	if !ok {
		return nil, errors.New("unknown kid")
	}
	return k, nil
}

// VerifierFactory builds a Verifier from settings:
//
//	issuer            required. Expected "iss".
//	audience          required. Comma-separated; the token must name one.
//	public_key_files  comma-separated paths to PKIX Ed25519 PEM files, or
//	public_keys_pem   the PEM blocks themselves. Exactly one is required.
//	                  Several keys are allowed for key rotation.
//	leeway            optional. Default 30s.
func VerifierFactory(s auth.Settings) (auth.Verifier, error) {
	issuer, err := s.Require("issuer")
	if err != nil {
		return nil, err
	}
	audience := s.List("audience")
	pemData, err := keyMaterial(s, "public_key_files", "public_keys_pem")
	if err != nil {
		return nil, err
	}
	keys, err := ParsePublicKeysPEM(pemData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrProviderConfig, err)
	}
	leeway, err := s.Duration("leeway", DefaultLeeway)
	if err != nil {
		return nil, err
	}
	return NewVerifier(issuer, keys, audience, WithLeeway(leeway))
}

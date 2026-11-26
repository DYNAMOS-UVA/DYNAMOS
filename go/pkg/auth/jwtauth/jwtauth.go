// Package jwtauth is the auth provider for DYNAMOS internal service
// tokens: short-lived JWTs signed with Ed25519 (alg "EdDSA").
//
// One component holds the private key and issues tokens (in the legacy
// flow: policy-enforcer, for each approved job and data steward). Every
// receiving service holds only the public key(s) and verifies locally.
//
// Token claims:
//
//	iss    issuer name, must match the verifier's configured issuer
//	sub    subject (the user the job runs for, or a service name)
//	aud    recipients (for example one data steward); always required
//	exp    expiry; always required
//	iat    issue time; nbf: not before
//	jti    unique token ID
//	kind   auth.Kind of the subject ("user", "service", "participant")
//	scope  space-separated scopes
//	attrs  string map of extra claims (for example {"jobId": "..."})
//
// The "kid" header is the RFC 7638 thumbprint of the public key, so no
// key ID needs to be configured. Verifiers accept several public keys for
// key rotation.
//
// Both factories register under the name "jwt". Blank-import to use:
//
//	import _ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
package jwtauth

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
)

// Name is the registry name of this provider.
const Name = "jwt"

const (
	// DefaultTTL is the token lifetime when a Grant gives none.
	DefaultTTL = 5 * time.Minute
	// DefaultMaxTTL caps every Grant.TTL.
	DefaultMaxTTL = time.Hour
	// DefaultLeeway is the allowed clock difference between services.
	DefaultLeeway = 30 * time.Second
)

// claims is the token payload.
type claims struct {
	jwt.RegisteredClaims
	Kind  auth.Kind         `json:"kind,omitempty"`
	Scope string            `json:"scope,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

func init() {
	auth.Register(Name, VerifierFactory)
	auth.RegisterIssuer(Name, IssuerFactory)
}

func validIssuerName(issuer string) error {
	if strings.TrimSpace(issuer) == "" {
		return fmt.Errorf("%w: issuer name is required", auth.ErrProviderConfig)
	}
	return nil
}

func scopeString(scopes []string) (string, error) {
	for _, s := range scopes {
		if s == "" || strings.ContainsAny(s, " \t\n") {
			return "", fmt.Errorf("jwtauth: scope %q is empty or has whitespace", s)
		}
	}
	return strings.Join(scopes, " "), nil
}

func cloneAttrs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	return maps.Clone(in)
}

var _ auth.Issuer = (*Issuer)(nil)
var _ auth.Verifier = (*Verifier)(nil)

package auth

import (
	"context"
	"slices"
	"time"
)

// Kind classifies who a Principal is.
type Kind string

const (
	// KindUser is a human end user (for example signed in through OIDC).
	KindUser Kind = "user"
	// KindService is a DYNAMOS component or job-scoped service token.
	KindService Kind = "service"
	// KindParticipant is an external dataspace participant (for example
	// identified by a DSP DAT and its DID).
	KindParticipant Kind = "participant"
)

// Principal is a verified identity. Only a Verifier creates one, and
// handlers must read identity from here, never from the request body.
type Principal struct {
	// Subject is the stable identifier inside Issuer (user name, service
	// name, DID, ...).
	Subject string
	// Issuer identifies who vouched for Subject (token "iss", IdP URL, DID).
	Issuer string
	// Kind classifies the principal.
	Kind Kind
	// Provider is the registry name of the provider that verified it.
	Provider string
	// Scopes are the permissions granted to this principal.
	Scopes []string
	// Audience lists the recipients the credential was issued for.
	Audience []string
	// ExpiresAt is when the credential stops being valid. Zero: unknown.
	ExpiresAt time.Time
	// Attributes holds provider-specific claims (for example "jobId").
	Attributes map[string]string
}

// HasScope reports whether the principal holds scope.
func (p Principal) HasScope(scope string) bool {
	return slices.Contains(p.Scopes, scope)
}

// HasAllScopes reports whether the principal holds every given scope.
// With no scopes given it returns true.
func (p Principal) HasAllScopes(scopes ...string) bool {
	for _, s := range scopes {
		if !p.HasScope(s) {
			return false
		}
	}
	return true
}

// HasAudience reports whether aud is one of the credential's audiences.
func (p Principal) HasAudience(aud string) bool {
	return slices.Contains(p.Audience, aud)
}

// Attribute returns the named attribute and whether it is present.
func (p Principal) Attribute(key string) (string, bool) {
	v, ok := p.Attributes[key]
	return v, ok
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx that carries p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the Principal stored in ctx by Middleware.
// The bool is false when the request was not authenticated.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

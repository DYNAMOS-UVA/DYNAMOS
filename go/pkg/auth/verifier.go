package auth

import (
	"context"
	"errors"
	"time"
)

// Verifier checks a raw credential and returns the identity behind it.
//
// Contract for implementations:
//   - Return a Principal only for a fully verified credential.
//   - Wrap a sentinel error (ErrInvalidCredentials, ErrUnsupportedToken,
//     ErrUnavailable) so callers can classify the failure.
//   - Return ErrUnsupportedToken when the token is not in this provider's
//     format, so Any() can try the next provider.
//   - Never put the token in an error message or a log line.
type Verifier interface {
	Verify(ctx context.Context, token string) (Principal, error)
}

// VerifierFunc adapts a function to the Verifier interface.
type VerifierFunc func(ctx context.Context, token string) (Principal, error)

// Verify calls f.
func (f VerifierFunc) Verify(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}

// Grant describes the token an Issuer must mint.
type Grant struct {
	// Audience lists the intended recipients (for example one steward).
	Audience []string
	// Scopes are the permissions the token carries.
	Scopes []string
	// TTL is the token lifetime. The Issuer may cap it.
	TTL time.Duration
	// Attributes are extra claims (for example "jobId").
	Attributes map[string]string
}

// Token is a credential minted by an Issuer.
type Token struct {
	// Value is the raw credential sent in "Authorization: Bearer <Value>".
	Value string
	// ExpiresAt is when the credential stops being valid.
	ExpiresAt time.Time
}

// Issuer mints credentials for subject that a matching Verifier accepts.
type Issuer interface {
	Issue(ctx context.Context, subject Principal, grant Grant) (Token, error)
}

// Any returns a Verifier that tries each verifier in order and returns
// the first success. A verifier that answers ErrUnsupportedToken is
// skipped. When none succeeds, the result is the first error that was not
// ErrUnsupportedToken, or ErrInvalidCredentials when every verifier
// declined the token.
//
// Use it when one endpoint accepts more than one credential type (for
// example a user OIDC token or an internal service token).
func Any(verifiers ...Verifier) Verifier {
	vs := nonNil(verifiers)
	return VerifierFunc(func(ctx context.Context, token string) (Principal, error) {
		var firstErr error
		for _, v := range vs {
			p, err := v.Verify(ctx, token)
			if err == nil {
				return p, nil
			}
			if errors.Is(err, ErrUnsupportedToken) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return Principal{}, firstErr
		}
		return Principal{}, ErrInvalidCredentials
	})
}

// DenyAll is a Verifier that rejects every credential. It is the safe
// value for a service that has no provider configured.
var DenyAll Verifier = VerifierFunc(func(context.Context, string) (Principal, error) {
	return Principal{}, ErrInvalidCredentials
})

func nonNil(in []Verifier) []Verifier {
	out := make([]Verifier, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}

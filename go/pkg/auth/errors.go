package auth

import "errors"

// Sentinel errors. Providers wrap these (fmt.Errorf("%w: ...", ErrX)) so
// callers can classify a failure with errors.Is without parsing text.
var (
	// ErrMissingCredentials: the request carries no usable credentials
	// (no Authorization header, wrong scheme, empty token).
	ErrMissingCredentials = errors.New("auth: missing credentials")

	// ErrInvalidCredentials: credentials are present but rejected
	// (bad signature, expired, wrong audience, unknown token).
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrUnsupportedToken: the provider does not recognize this token
	// format and makes no claim about it. Any() uses this to try the next
	// provider. Outside Any() it is treated as ErrInvalidCredentials.
	ErrUnsupportedToken = errors.New("auth: unsupported token")

	// ErrForbidden: the caller is authenticated but lacks permission.
	ErrForbidden = errors.New("auth: forbidden")

	// ErrUnavailable: the provider cannot decide now because a dependency
	// failed (for example a JWKS or DID document fetch). Not the caller's
	// fault. The middleware fails closed and answers 503.
	ErrUnavailable = errors.New("auth: provider unavailable")

	// ErrUnknownProvider: a Registry has no Factory for the given name.
	ErrUnknownProvider = errors.New("auth: unknown provider")

	// ErrProviderConfig: a Factory rejected its Settings.
	ErrProviderConfig = errors.New("auth: invalid provider configuration")
)

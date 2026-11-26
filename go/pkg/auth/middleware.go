package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrorHandler writes the response for a failed authentication or
// authorization. err wraps one of the package sentinel errors.
type ErrorHandler func(w http.ResponseWriter, r *http.Request, err error)

type middlewareConfig struct {
	realm   string
	onError ErrorHandler
}

// MiddlewareOption configures Middleware and RequireScope.
type MiddlewareOption func(*middlewareConfig)

// WithRealm sets the realm sent in the WWW-Authenticate header.
// Default: "dynamos".
func WithRealm(realm string) MiddlewareOption {
	return func(c *middlewareConfig) { c.realm = realm }
}

// WithErrorHandler replaces the default error response. Use it to log the
// failure (the error never contains the token) and then call
// DefaultErrorHandler, or to write a service-specific error body.
func WithErrorHandler(h ErrorHandler) MiddlewareOption {
	return func(c *middlewareConfig) {
		if h != nil {
			c.onError = h
		}
	}
}

func newMiddlewareConfig(opts []MiddlewareOption) *middlewareConfig {
	c := &middlewareConfig{realm: "dynamos"}
	for _, o := range opts {
		o(c)
	}
	if c.onError == nil {
		realm := c.realm
		c.onError = func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, realm, err)
		}
	}
	return c
}

// BearerToken returns the token from an "Authorization: Bearer <token>"
// header. The scheme is matched without regard to case (RFC 7235).
// It returns ErrMissingCredentials when the header is absent, uses
// another scheme, or holds an empty or malformed token.
func BearerToken(r *http.Request) (string, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", fmt.Errorf("%w: no Authorization header", ErrMissingCredentials)
	}
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return "", fmt.Errorf("%w: Authorization scheme is not Bearer", ErrMissingCredentials)
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", fmt.Errorf("%w: malformed bearer token", ErrMissingCredentials)
	}
	return token, nil
}

// Middleware authenticates every request with v. On success the Principal
// is stored in the request context (see PrincipalFrom) and next is called.
// On failure next is not called and the error handler answers:
// 401 for missing or rejected credentials, 503 when the provider is
// unavailable. A nil v denies every request.
//
// Middleware does not handle CORS preflight. Put the CORS handler outside
// it, as the services already do with gorilla/handlers.CORS.
func Middleware(v Verifier, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	if v == nil {
		v = DenyAll
	}
	cfg := newMiddlewareConfig(opts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := BearerToken(r)
			if err != nil {
				cfg.onError(w, r, err)
				return
			}
			p, err := v.Verify(r.Context(), token)
			if err != nil {
				cfg.onError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// RequireScope authorizes a request that Middleware already authenticated.
// It answers 401 when the context has no Principal and 403 when the
// Principal lacks any of the scopes.
func RequireScope(scopes []string, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	required := append([]string(nil), scopes...)
	cfg := newMiddlewareConfig(opts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				cfg.onError(w, r, fmt.Errorf("%w: request is not authenticated", ErrMissingCredentials))
				return
			}
			if !p.HasAllScopes(required...) {
				cfg.onError(w, r, fmt.Errorf("%w: missing required scope", ErrForbidden))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// DefaultErrorHandler returns the package's standard error writer for
// realm, for use inside a custom ErrorHandler.
func DefaultErrorHandler(realm string) ErrorHandler {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, realm, err)
	}
}

// StatusCode maps an authentication error to its HTTP status.
func StatusCode(err error) int {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	default:
		// Missing, invalid, unsupported and unclassified errors all fail
		// closed as 401.
		return http.StatusUnauthorized
	}
}

// writeError answers with a fixed message per status. Provider details are
// not sent to the client.
func writeError(w http.ResponseWriter, realm string, err error) {
	status := StatusCode(err)
	switch status {
	case http.StatusUnauthorized:
		challenge := fmt.Sprintf("Bearer realm=%q", realm)
		if !errors.Is(err, ErrMissingCredentials) {
			challenge += `, error="invalid_token"`
		}
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "unauthorized", status)
	case http.StatusForbidden:
		w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer realm=%q, error=\"insufficient_scope\"", realm))
		http.Error(w, "forbidden", status)
	default:
		http.Error(w, "authentication unavailable", status)
	}
}

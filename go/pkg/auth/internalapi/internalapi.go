// Package internalapi protects the internal HTTP API of a DYNAMOS service
// (the /internal/v1/... routes of the DSP services). It puts every route
// behind one auth rule and leaves only /health open.
//
// The rule: a valid token from the configured verifier, with the required
// scope, and of an allowed kind. A participant token must not reach an
// internal API: these APIs trust their caller for the participant of a
// transfer or negotiation.
package internalapi

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
)

// Config describes the rule for one service.
type Config struct {
	// Verifier checks the bearer token. Nil denies every request.
	Verifier auth.Verifier
	// Scope is the scope every request needs.
	Scope string
	// Kinds are the principal kinds that may call the API. Empty means
	// only auth.KindService.
	Kinds []auth.Kind
	// Realm is sent in the WWW-Authenticate header.
	Realm string
	// OnReject is called for each refused request, before the response is
	// written. Use it to log. err never contains the token.
	OnReject func(r *http.Request, err error)
}

// Handler returns the HTTP handler of the service: health on /health
// without authentication, and internal behind the rule for every other
// path (also for paths that internal does not know).
func Handler(c Config, health http.HandlerFunc, internal http.Handler) http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("/health", health)
	root.Handle("/", Protect(c)(internal))
	return root
}

// Protect returns a middleware that applies the rule of c.
func Protect(c Config) func(http.Handler) http.Handler {
	kinds := c.Kinds
	if len(kinds) == 0 {
		kinds = []auth.Kind{auth.KindService}
	}
	onError := func(w http.ResponseWriter, r *http.Request, err error) {
		if c.OnReject != nil {
			c.OnReject(r, err)
		}
		auth.DefaultErrorHandler(c.Realm)(w, r, err)
	}
	opts := []auth.MiddlewareOption{auth.WithRealm(c.Realm), auth.WithErrorHandler(onError)}
	authenticate := auth.Middleware(c.Verifier, opts...)
	authorize := auth.RequireScope([]string{c.Scope}, opts...)
	return func(next http.Handler) http.Handler {
		kindCheck := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := auth.PrincipalFrom(r.Context())
			if !slices.Contains(kinds, p.Kind) {
				onError(w, r, fmt.Errorf("%w: %s token on the internal API", auth.ErrForbidden, p.Kind))
				return
			}
			next.ServeHTTP(w, r)
		})
		return authenticate(authorize(kindCheck))
	}
}

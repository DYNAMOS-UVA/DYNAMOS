// Package staticdev is an auth provider for local development only. It
// accepts one fixed shared token and maps it to a configured Principal.
//
// It is compiled in a disabled state by default. The factory refuses to
// build unless the binary was built with the "authdev" build tag
// (go build -tags authdev). Production images never set that tag, so a
// configuration mistake cannot turn on a fixed shared token in production.
//
// Settings:
//
//	token    required. The shared bearer token.
//	subject  required. Principal.Subject for the token.
//	kind     optional. "user", "service" (default) or "participant".
//	scopes   optional. Comma-separated scope list.
//	issuer   optional. Principal.Issuer. Default "static-dev".
//
// Register it with a blank import:
//
//	import _ "github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/staticdev"
package staticdev

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
)

// Name is the registry name of this provider.
const Name = "static-dev"

// ErrDisabled is returned by the factory in a binary built without the
// "authdev" build tag.
var ErrDisabled = errors.New("staticdev: provider is disabled in this build (build with -tags authdev)")

func init() {
	auth.Register(Name, Factory)
}

// Factory builds the provider from settings. It refuses in a build
// without the "authdev" tag.
func Factory(s auth.Settings) (auth.Verifier, error) {
	if !compiledIn {
		return nil, ErrDisabled
	}
	return newVerifier(s)
}

type verifier struct {
	digest    [sha256.Size]byte
	principal auth.Principal
}

// newVerifier skips the build-tag check. Only the factory and the tests
// in this package call it.
func newVerifier(s auth.Settings) (*verifier, error) {
	token, err := s.Require("token")
	if err != nil {
		return nil, err
	}
	subject, err := s.Require("subject")
	if err != nil {
		return nil, err
	}
	kind := auth.KindService
	if k := strings.TrimSpace(s.Get("kind")); k != "" {
		switch auth.Kind(k) {
		case auth.KindUser, auth.KindService, auth.KindParticipant:
			kind = auth.Kind(k)
		default:
			return nil, fmt.Errorf("%w: unknown kind %q", auth.ErrProviderConfig, k)
		}
	}
	issuer := strings.TrimSpace(s.Get("issuer"))
	if issuer == "" {
		issuer = Name
	}
	return &verifier{
		digest: sha256.Sum256([]byte(token)),
		principal: auth.Principal{
			Subject:  subject,
			Issuer:   issuer,
			Kind:     kind,
			Provider: Name,
			Scopes:   splitList(s.Get("scopes")),
		},
	}, nil
}

// Verify compares the token in constant time. A mismatch answers
// ErrUnsupportedToken so that auth.Any can try other providers.
func (v *verifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	got := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(got[:], v.digest[:]) != 1 {
		return auth.Principal{}, fmt.Errorf("%w: not the static-dev token", auth.ErrUnsupportedToken)
	}
	p := v.principal
	p.Scopes = append([]string(nil), v.principal.Scopes...)
	return p, nil
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

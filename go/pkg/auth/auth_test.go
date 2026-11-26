package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodToken = "good-token"

// fakeVerifier accepts goodToken and answers with err for anything else.
func fakeVerifier(name string, err error) Verifier {
	return VerifierFunc(func(_ context.Context, token string) (Principal, error) {
		if token == goodToken {
			return Principal{Subject: name, Provider: name, Scopes: []string{"read"}}, nil
		}
		return Principal{}, err
	})
}

// echoHandler writes the authenticated subject, or fails the test when it
// runs without a Principal.
func echoHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Error("handler reached without a Principal")
		}
		io.WriteString(w, p.Subject)
	})
}

func do(h http.Handler, authz string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"BEARER   abc  ", "abc", true},
		{"", "", false},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"Basic abc", "", false},
		{"Bearer a b", "", false},
		{"abc", "", false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%q", c.header), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			got, err := BearerToken(req)
			if c.ok {
				require.NoError(t, err)
				assert.Equal(t, c.want, got)
			} else {
				assert.ErrorIs(t, err, ErrMissingCredentials)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	h := Middleware(fakeVerifier("svc", ErrInvalidCredentials))(echoHandler(t))

	t.Run("success puts principal in context", func(t *testing.T) {
		rec := do(h, "Bearer "+goodToken)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "svc", rec.Body.String())
	})

	t.Run("missing header", func(t *testing.T) {
		rec := do(h, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, `Bearer realm="dynamos"`, rec.Header().Get("WWW-Authenticate"))
	})

	t.Run("wrong scheme", func(t *testing.T) {
		rec := do(h, "Basic "+goodToken)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("rejected token does not leak", func(t *testing.T) {
		rec := do(h, "Bearer secret-value")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
		assert.NotContains(t, rec.Body.String(), "secret-value")
	})
}

func TestMiddlewareStatusMapping(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"unavailable":  {fmt.Errorf("%w: jwks down", ErrUnavailable), http.StatusServiceUnavailable},
		"unsupported":  {ErrUnsupportedToken, http.StatusUnauthorized},
		"forbidden":    {ErrForbidden, http.StatusForbidden},
		"unclassified": {errors.New("boom"), http.StatusUnauthorized},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := Middleware(fakeVerifier("svc", c.err))(echoHandler(t))
			rec := do(h, "Bearer other")
			assert.Equal(t, c.want, rec.Code)
			assert.NotContains(t, rec.Body.String(), "jwks down")
		})
	}
}

func TestMiddlewareNilVerifierDenies(t *testing.T) {
	h := Middleware(nil)(echoHandler(t))
	assert.Equal(t, http.StatusUnauthorized, do(h, "Bearer "+goodToken).Code)
}

func TestMiddlewareOptions(t *testing.T) {
	var seen error
	h := Middleware(DenyAll,
		WithRealm("orchestrator"),
		WithErrorHandler(func(w http.ResponseWriter, r *http.Request, err error) {
			seen = err
			DefaultErrorHandler("orchestrator")(w, r, err)
		}),
	)(echoHandler(t))

	rec := do(h, "Bearer x")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.ErrorIs(t, seen, ErrInvalidCredentials)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `realm="orchestrator"`)
}

func TestRequireScope(t *testing.T) {
	v := fakeVerifier("svc", ErrInvalidCredentials) // grants scope "read"

	t.Run("granted", func(t *testing.T) {
		h := Middleware(v)(RequireScope([]string{"read"})(echoHandler(t)))
		assert.Equal(t, http.StatusOK, do(h, "Bearer "+goodToken).Code)
	})

	t.Run("missing scope", func(t *testing.T) {
		h := Middleware(v)(RequireScope([]string{"read", "write"})(echoHandler(t)))
		rec := do(h, "Bearer "+goodToken)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "insufficient_scope")
	})

	t.Run("not authenticated", func(t *testing.T) {
		h := RequireScope([]string{"read"})(echoHandler(t))
		assert.Equal(t, http.StatusUnauthorized, do(h, "Bearer "+goodToken).Code)
	})
}

func TestAny(t *testing.T) {
	declines := fakeVerifier("a", ErrUnsupportedToken)
	rejects := fakeVerifier("b", fmt.Errorf("%w: bad sig", ErrInvalidCredentials))
	accepts := VerifierFunc(func(context.Context, string) (Principal, error) {
		return Principal{Subject: "c"}, nil
	})
	ctx := context.Background()

	p, err := Any(declines, nil, accepts).Verify(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, "c", p.Subject)

	_, err = Any(declines, rejects).Verify(ctx, "x")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Contains(t, err.Error(), "bad sig", "keeps the first real rejection")

	_, err = Any(declines, declines).Verify(ctx, "x")
	assert.ErrorIs(t, err, ErrInvalidCredentials)

	_, err = Any().Verify(ctx, "x")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestPrincipal(t *testing.T) {
	p := Principal{
		Scopes:     []string{"a", "b"},
		Audience:   []string{"UVA"},
		Attributes: map[string]string{"jobId": "j1"},
	}
	assert.True(t, p.HasScope("a"))
	assert.False(t, p.HasScope("c"))
	assert.True(t, p.HasAllScopes())
	assert.True(t, p.HasAllScopes("a", "b"))
	assert.False(t, p.HasAllScopes("a", "c"))
	assert.True(t, p.HasAudience("UVA"))
	assert.False(t, p.HasAudience("VU"))
	v, ok := p.Attribute("jobId")
	assert.True(t, ok)
	assert.Equal(t, "j1", v)

	_, ok = PrincipalFrom(context.Background())
	assert.False(t, ok)
	got, ok := PrincipalFrom(WithPrincipal(context.Background(), Principal{Subject: "s"}))
	assert.True(t, ok)
	assert.Equal(t, "s", got.Subject)
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	factory := func(s Settings) (Verifier, error) {
		if _, err := s.Require("need"); err != nil {
			return nil, err
		}
		return fakeVerifier(s.Get("need"), ErrUnsupportedToken), nil
	}

	require.NoError(t, r.Register("fake", factory))
	require.NoError(t, r.Register("other", factory))
	assert.Error(t, r.Register("fake", factory), "duplicate")
	assert.Error(t, r.Register("", factory), "empty name")
	assert.Error(t, r.Register("nil", nil), "nil factory")
	assert.Equal(t, []string{"fake", "other"}, r.Names())

	_, err := r.Build("missing", nil)
	assert.ErrorIs(t, err, ErrUnknownProvider)

	_, err = r.Build("fake", nil)
	assert.ErrorIs(t, err, ErrProviderConfig)

	v, err := r.Build("fake", Settings{"need": "svc"})
	require.NoError(t, err)
	p, err := v.Verify(context.Background(), goodToken)
	require.NoError(t, err)
	assert.Equal(t, "svc", p.Subject)
}

func TestRegistryBuildAll(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register("fake", func(s Settings) (Verifier, error) {
		return fakeVerifier(s.Get("name"), ErrUnsupportedToken), nil
	}))

	_, err := r.BuildAll()
	assert.ErrorIs(t, err, ErrProviderConfig, "no providers must not mean no auth")

	_, err = r.BuildAll(ProviderSpec{Name: "fake"}, ProviderSpec{Name: "missing"})
	assert.ErrorIs(t, err, ErrUnknownProvider)

	v, err := r.BuildAll(ProviderSpec{Name: "fake", Settings: Settings{"name": "first"}},
		ProviderSpec{Name: "fake", Settings: Settings{"name": "second"}})
	require.NoError(t, err)
	p, err := v.Verify(context.Background(), goodToken)
	require.NoError(t, err)
	assert.Equal(t, "first", p.Subject)
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	saved := DefaultRegistry
	t.Cleanup(func() { DefaultRegistry = saved })
	DefaultRegistry = NewRegistry()

	f := func(Settings) (Verifier, error) { return DenyAll, nil }
	Register("x", f)
	assert.Panics(t, func() { Register("x", f) })
}

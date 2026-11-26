package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCORSOrigins(t *testing.T) {
	got, err := ParseCORSOrigins(" https://dynamos.example.org/ , http://localhost:3000,, ")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://dynamos.example.org", "http://localhost:3000"}, got)

	got, err = ParseCORSOrigins("")
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = ParseCORSOrigins("*")
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, got)

	for _, bad := range []string{"localhost:3000", "ftp://x.org", "https://x.org/app", "https://x.org?a=1", "https://u:p@x.org", "https://"} {
		_, err := ParseCORSOrigins(bad)
		assert.Error(t, err, bad)
	}
}

func corsRequest(h http.Handler, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	req.Header.Set("Origin", origin)
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", "PUT")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("no origins sends no CORS headers", func(t *testing.T) {
		rec := corsRequest(CORS(nil)(ok), http.MethodGet, "https://evil.example")
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("allowed origin", func(t *testing.T) {
		h := CORS([]string{"https://dynamos.example.org"})(ok)
		rec := corsRequest(h, http.MethodGet, "https://dynamos.example.org")
		assert.Equal(t, "https://dynamos.example.org", rec.Header().Get("Access-Control-Allow-Origin"))

		rec = corsRequest(h, http.MethodOptions, "https://dynamos.example.org")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "https://dynamos.example.org", rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("other origin", func(t *testing.T) {
		h := CORS([]string{"https://dynamos.example.org"})(ok)
		rec := corsRequest(h, http.MethodGet, "https://evil.example")
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})
}

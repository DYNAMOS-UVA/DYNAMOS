package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/handlers"
)

// CORSOriginsEnv lists the browser origins a service accepts, comma
// separated, for example "https://dynamos.example.org,http://localhost:3000".
// Unset or empty: no CORS headers, so browsers block cross-origin calls.
// Server-to-server callers do not use CORS and are not affected.
const CORSOriginsEnv = "CORS_ALLOWED_ORIGINS"

// CORSOriginsFromEnv reads and validates CORSOriginsEnv.
func CORSOriginsFromEnv() ([]string, error) {
	return ParseCORSOrigins(os.Getenv(CORSOriginsEnv))
}

// ParseCORSOrigins splits a comma-separated origin list and validates each
// entry: "*" or scheme://host[:port], with no path, query or fragment.
func ParseCORSOrigins(raw string) ([]string, error) {
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o != "*" {
			u, err := url.Parse(o)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
				(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
				return nil, fmt.Errorf("%s: %q is not an origin (scheme://host[:port])", CORSOriginsEnv, o)
			}
			o = strings.TrimSuffix(o, "/")
		}
		origins = append(origins, o)
	}
	return origins, nil
}

// CORS returns middleware that allows the given origins. With no origins
// it returns the handler unchanged. This matters because gorilla's CORS
// handler treats an empty origin list as "allow every origin".
func CORS(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		return func(h http.Handler) http.Handler { return h }
	}
	return handlers.CORS(
		handlers.AllowedOrigins(origins),
		handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"}),
		handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS"}),
	)
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// internalServiceErrorBody is the {code,error} JSON shape every DYNAMOS
// internal-API service in this package calls uses for non-2xx responses -
// catalog-service's internalError, negotiation-service's internalError.
type internalServiceErrorBody struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// mapInternalServiceError decodes resp's {code,error} body and returns the
// sentinel codeMap[code] maps to, or a generic error wrapping the raw
// code/message - for an unparseable body, or a code with no codeMap entry.
// Shared by catalog_client.go and negotiation_client.go's own error-mapping
// functions, which otherwise duplicated this decode-then-switch verbatim.
func mapInternalServiceError(serviceName string, resp *http.Response, codeMap map[string]error) error {
	var body internalServiceErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("%s returned %d with unparseable body: %w", serviceName, resp.StatusCode, err)
	}

	if sentinel, ok := codeMap[body.Code]; ok {
		return sentinel
	}
	return fmt.Errorf("%s returned %d (%s): %s", serviceName, resp.StatusCode, body.Code, body.Error)
}

// requireMethod writes a 405 (with the correct Allow header) and returns
// false if r's method isn't want - shared by every handler in this package
// that only accepts one HTTP method, which otherwise repeated this 4-line
// guard verbatim in every handler.
func requireMethod(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method == want {
		return true
	}
	w.Header().Set("Allow", want)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

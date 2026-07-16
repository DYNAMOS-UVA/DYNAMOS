package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
)

// catalogRequestMessage mirrors the DSP CatalogRequestMessage shape
// (docs/catalog/spec-reference/catalog/example/catalog-request-message.json).
// Filter is accepted (so a well-formed request round-trips) but intentionally
// unused - issue #10 scopes this endpoint to minimal/no validation, and
// DYNAMOS's config-driven catalog has nothing to filter against yet.
type catalogRequestMessage struct {
	Context interface{}   `json:"@context"`
	Type    string        `json:"@type"`
	Filter  []interface{} `json:"filter"`
}

// catalogError mirrors the DSP CatalogError shape
// (docs/catalog/spec-reference/catalog/example/catalog-error.json).
type catalogError struct {
	Context []interface{} `json:"@context"`
	Type    string        `json:"@type"`
	Code    string        `json:"code"`
	Reason  []string      `json:"reason"`
}

func writeCatalogError(w http.ResponseWriter, status int, code string, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(catalogError{
		Context: catalog.Context,
		Type:    "CatalogError",
		Code:    code,
		Reason:  []string{reason},
	})
}

// participantFromRequest extracts the requesting participant's identity from
// the Authorization header. This is a deliberate placeholder for Phase 1:
// the header value (after stripping an optional "Bearer " prefix) is used
// directly as the participant identity DYNAMOS's Relation map is keyed by
// (an email) - with no cryptographic verification. Real DSP identity/token
// handling is out of scope for issue #10's "minimal/no validation" ask.
func participantFromRequest(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", false
	}
	return strings.TrimPrefix(auth, "Bearer "), true
}

// catalogRequestHandler implements POST /catalog/request per the DSP Catalog
// HTTPS Binding (docs/catalog/spec-reference/specifications/catalog.binding.https.md):
// reads the incoming Catalog Request Message, resolves the requesting
// participant, and returns the DCAT-compliant JSON-LD Catalog built from the
// config loaded at startup (see loadCatalogConfig in main.go).
func catalogRequestHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var reqMsg catalogRequestMessage
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&reqMsg); err != nil && !errors.Is(err, io.EOF) {
			writeCatalogError(w, http.StatusBadRequest, "invalid-request", "Request body is not a valid Catalog Request Message: "+err.Error())
			return
		}
	}

	participant, ok := participantFromRequest(r)
	if !ok {
		writeCatalogError(w, http.StatusUnauthorized, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	cat, err := fetchCatalog(participant)
	if err != nil {
		if errors.Is(err, ErrParticipantNotFound) {
			logger.Sugar().Infow("Catalog request denied", "participant", participant, "error", err)
			// Per the DSP spec's own CatalogError example: an unrecognized
			// requester is reported as "not provisioned", not a hard 4xx
			// client error like a malformed request.
			writeCatalogError(w, http.StatusForbidden, "not-provisioned", "Catalog not provisioned for this requester.")
			return
		}
		logger.Sugar().Errorw("catalog-service request failed", "participant", participant, "error", err)
		writeCatalogError(w, http.StatusBadGateway, "upstream-error", "Failed to retrieve catalog data.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(cat)
}

// catalogDatasetHandler implements GET /catalog/datasets/:id per the DSP
// Catalog Protocol's Dataset Request Message - the second required message
// type alongside Catalog Request (catalogRequestHandler above). Resolves the
// requesting participant the same way, then returns the single Dataset
// matching :id if it's visible to them, or a CatalogError otherwise.
func catalogDatasetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	participant, ok := participantFromRequest(r)
	if !ok {
		writeCatalogError(w, http.StatusUnauthorized, "missing-authorization", "An Authorization header identifying the requesting participant is required.")
		return
	}

	datasetID := r.PathValue("id")
	ds, err := fetchDataset(participant, datasetID)
	if err != nil {
		if errors.Is(err, ErrParticipantNotFound) || errors.Is(err, ErrDatasetNotFound) {
			logger.Sugar().Infow("Dataset request denied", "participant", participant, "dataset", datasetID, "error", err)
			writeCatalogError(w, http.StatusNotFound, "not-found", "Dataset not found or not provisioned for this requester.")
			return
		}
		logger.Sugar().Errorw("catalog-service request failed", "participant", participant, "dataset", datasetID, "error", err)
		writeCatalogError(w, http.StatusBadGateway, "upstream-error", "Failed to retrieve catalog data.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(ds)
}

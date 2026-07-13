package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
)

// internalError is this service's own internal-API error shape - not the DSP
// CatalogError shape, since this contract is service-to-service only.
// dsp-connector (issue #29) is expected to map Code into a DSP CatalogError.
type internalError struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeInternalError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(internalError{Code: code, Error: msg})
}

// fetchConfigOrError runs fetchConfig and writes the right internal-API
// error response on failure: participant-not-found is a business 404,
// anything else (etcd I/O) is a 500 - the caller only needs to check ok.
func fetchConfigOrError(w http.ResponseWriter, participant string) (*catalog.Config, bool) {
	cfg, err := fetchConfig(etcdClient, party, participant)
	if err == nil {
		return cfg, true
	}

	if errors.Is(err, ErrParticipantNotFound) {
		writeInternalError(w, http.StatusNotFound, "participant-not-found", err.Error())
		return nil, false
	}

	logger.Sugar().Errorw("failed to fetch catalog config", "participant", participant, "error", err)
	writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to fetch catalog data")
	return nil, false
}

// internalCatalogHandler implements GET /internal/v1/catalog?participant={email}
// (issue #28) - the internal counterpart to dsp-connector's DSP-facing
// catalogRequestHandler, backed by live etcd data instead of a static config.
func internalCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	participant := r.URL.Query().Get("participant")
	if participant == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-participant", "participant query parameter is required")
		return
	}

	cfg, ok := fetchConfigOrError(w, participant)
	if !ok {
		return
	}

	// cfg.Relations only ever holds participant's own relation (buildConfig's
	// construction), so BuildCatalog cannot fail here with a fresh cfg.
	cat, err := catalog.BuildCatalog(cfg, participant)
	if err != nil {
		logger.Sugar().Errorw("BuildCatalog failed after successful fetch", "participant", participant, "error", err)
		writeInternalError(w, http.StatusInternalServerError, "internal-error", "failed to build catalog")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(cat)
}

// internalDatasetHandler implements
// GET /internal/v1/catalog/datasets/{id}?participant={email} (issue #28),
// returning the same RootDataset shape dsp-connector's own dataset endpoint
// already returns today.
func internalDatasetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	participant := r.URL.Query().Get("participant")
	if participant == "" {
		writeInternalError(w, http.StatusBadRequest, "missing-participant", "participant query parameter is required")
		return
	}

	cfg, ok := fetchConfigOrError(w, participant)
	if !ok {
		return
	}

	datasetID := r.PathValue("id")
	ds, err := catalog.BuildDataset(cfg, participant, datasetID)
	if err != nil {
		// Participant presence already validated by fetchConfigOrError, so
		// this can only be an unknown datasetID.
		writeInternalError(w, http.StatusNotFound, "dataset-not-found", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(catalog.RootDataset{Context: catalog.Context, Dataset: *ds})
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
)

// ErrParticipantNotFound / ErrDatasetNotFound: sentinels so handlers can map
// catalog-service's internal-API error codes to the right DSP CatalogError.
var (
	ErrParticipantNotFound = errors.New("catalog-service: participant not found")
	ErrDatasetNotFound     = errors.New("catalog-service: dataset not found")
)

// internalErrorResponse mirrors catalog-service's own internalError shape
// (go/cmd/catalog-service/catalog_handler.go).
type internalErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

var catalogServiceClient = &http.Client{Timeout: 5 * time.Second}

// errorFromResponse maps a non-200 catalog-service response to a sentinel
// error, or a generic wrapped error for anything unexpected (etcd I/O
// failures on catalog-service's side, network errors, etc).
func errorFromResponse(resp *http.Response) error {
	var ie internalErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&ie); err != nil {
		return fmt.Errorf("catalog-service returned %d with unparseable body: %w", resp.StatusCode, err)
	}

	switch ie.Code {
	case "participant-not-found":
		return ErrParticipantNotFound
	case "dataset-not-found":
		return ErrDatasetNotFound
	default:
		return fmt.Errorf("catalog-service returned %d (%s): %s", resp.StatusCode, ie.Code, ie.Error)
	}
}

// fetchCatalog calls catalog-service's GET /internal/v1/catalog (issue #28)
// for participantEmail, replacing the old catalog.LoadConfig + BuildCatalog
// path (issue #9) now that catalog data lives in etcd, not a static file.
func fetchCatalog(participantEmail string) (*catalog.Catalog, error) {
	reqURL := fmt.Sprintf("%s/internal/v1/catalog?participant=%s", catalogServiceURL, url.QueryEscape(participantEmail))
	resp, err := catalogServiceClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("calling catalog-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errorFromResponse(resp)
	}

	var cat catalog.Catalog
	if err := json.NewDecoder(resp.Body).Decode(&cat); err != nil {
		return nil, fmt.Errorf("decoding catalog-service response: %w", err)
	}
	return &cat, nil
}

// fetchDataset calls catalog-service's
// GET /internal/v1/catalog/datasets/{id} (issue #28), replacing the old
// catalog.BuildDataset path (issue #22).
func fetchDataset(participantEmail, datasetID string) (*catalog.RootDataset, error) {
	reqURL := fmt.Sprintf("%s/internal/v1/catalog/datasets/%s?participant=%s", catalogServiceURL, url.PathEscape(datasetID), url.QueryEscape(participantEmail))
	resp, err := catalogServiceClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("calling catalog-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errorFromResponse(resp)
	}

	var ds catalog.RootDataset
	if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
		return nil, fmt.Errorf("decoding catalog-service response: %w", err)
	}
	return &ds, nil
}

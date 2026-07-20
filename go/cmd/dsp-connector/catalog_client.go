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

var catalogServiceClient = &http.Client{Timeout: 5 * time.Second}

// catalogServiceErrorCodes maps catalog-service's internal-API error codes
// (go/cmd/catalog-service/catalog_handler.go) to this package's sentinels.
var catalogServiceErrorCodes = map[string]error{
	"participant-not-found": ErrParticipantNotFound,
	"dataset-not-found":     ErrDatasetNotFound,
}

// errorFromResponse maps a non-200 catalog-service response to a sentinel
// error, or a generic wrapped error for anything unexpected (etcd I/O
// failures on catalog-service's side, network errors, etc).
func errorFromResponse(resp *http.Response) error {
	return mapInternalServiceError("catalog-service", resp, catalogServiceErrorCodes, nil)
}

// fetchCatalog calls catalog-service's GET /internal/v1/catalog (issue #28)
// for participant (an email for a non-DSP-adjacent caller, or a verified
// DID for a real DSP request post-#56 - see dat_verification.go), replacing
// the old catalog.LoadConfig + BuildCatalog path (issue #9) now that catalog
// data lives in etcd, not a static file.
func fetchCatalog(participant string) (*catalog.Catalog, error) {
	reqURL := fmt.Sprintf("%s/internal/v1/catalog?participant=%s", catalogServiceURL, url.QueryEscape(participant))
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
func fetchDataset(participant, datasetID string) (*catalog.RootDataset, error) {
	reqURL := fmt.Sprintf("%s/internal/v1/catalog/datasets/%s?participant=%s", catalogServiceURL, url.PathEscape(datasetID), url.QueryEscape(participant))
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

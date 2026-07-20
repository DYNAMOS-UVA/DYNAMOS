package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureCatalog/fixtureRootDataset match the VU/wageGap worked example used
// throughout this repo's catalog tests (same data as
// configuration/etcd_launch_files/{agreements,datasets}.json).
func fixtureCatalog() catalog.Catalog {
	return catalog.Catalog{
		Context:       catalog.Context,
		ID:            "urn:dynamos:catalog:VU:for-jorrit.stutterheim@cloudnation.nl",
		Type:          "Catalog",
		ParticipantID: "urn:dynamos:party:VU",
		Dataset: []catalog.Dataset{
			{
				ID:   "urn:dynamos:dataset:VU:wageGap",
				Type: "Dataset",
				HasPolicy: []catalog.Offer{
					{ID: "urn:dynamos:offer:VU:GUID", Type: "Offer", Assigner: "urn:dynamos:party:VU", Assignee: "mailto:jorrit.stutterheim@cloudnation.nl"},
				},
			},
		},
	}
}

func fixtureRootDataset(id string) catalog.RootDataset {
	return catalog.RootDataset{
		Context: catalog.Context,
		Dataset: catalog.Dataset{
			ID:   id,
			Type: "Dataset",
			// Real catalog-service's dataset endpoint always includes
			// HasPolicy (pkg/catalog.BuildDataset), same as the catalog
			// endpoint - kept in sync with fixtureCatalog()'s wageGap entry.
			HasPolicy: []catalog.Offer{
				{ID: "urn:dynamos:offer:VU:GUID", Type: "Offer", Assigner: "urn:dynamos:party:VU", Assignee: "mailto:jorrit.stutterheim@cloudnation.nl"},
			},
		},
	}
}

// startFixtureCatalogService stands in for a real catalog-service (issue
// #28's internal API), so these tests don't need one running - only the
// live regression check (TCK re-run) exercises the real service.
func startFixtureCatalogService(t *testing.T) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		participant := r.URL.Query().Get("participant")
		w.Header().Set("Content-Type", "application/json")
		if participant != "jorrit.stutterheim@cloudnation.nl" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "participant-not-found", "error": "no relation found"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(fixtureCatalog())
	})
	mux.HandleFunc("/internal/v1/catalog/datasets/{id}", func(w http.ResponseWriter, r *http.Request) {
		participant := r.URL.Query().Get("participant")
		w.Header().Set("Content-Type", "application/json")
		if participant != "jorrit.stutterheim@cloudnation.nl" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "participant-not-found", "error": "no relation found"})
			return
		}
		id := r.PathValue("id")
		if id != "urn:dynamos:dataset:VU:wageGap" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"code": "dataset-not-found", "error": "unknown dataset"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(fixtureRootDataset(id))
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	catalogServiceURL = ts.URL
}

func TestCatalogRequestHandler_KnownParticipant(t *testing.T) {
	startFixtureCatalogService(t)

	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"CatalogRequestMessage","filter":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString(body))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var cat catalog.Catalog
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cat))
	assert.Equal(t, "Catalog", cat.Type)
	assert.Equal(t, "urn:dynamos:catalog:VU:for-jorrit.stutterheim@cloudnation.nl", cat.ID)
	require.Len(t, cat.Dataset, 1)
	assert.Equal(t, "urn:dynamos:dataset:VU:wageGap", cat.Dataset[0].ID)
}

func TestCatalogRequestHandler_EmptyBodyStillWorks(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", nil)
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCatalogRequestHandler_UnknownParticipant(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", testAuthHeader("nobody@example.com"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "CatalogError", ce.Type)
	assert.Equal(t, "not-provisioned", ce.Code)
}

func TestCatalogRequestHandler_MissingAuthorization(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{}"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "missing-authorization", ce.Code)
}

func TestCatalogRequestHandler_WrongMethod(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/request", nil)
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

func TestCatalogDatasetHandler_KnownDataset(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/datasets/urn:dynamos:dataset:VU:wageGap", nil)
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("id", "urn:dynamos:dataset:VU:wageGap")
	rec := httptest.NewRecorder()

	catalogDatasetHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var ds catalog.RootDataset
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ds))
	assert.Equal(t, "urn:dynamos:dataset:VU:wageGap", ds.ID)
	assert.Equal(t, "Dataset", ds.Type)
	assert.NotEmpty(t, ds.Context)
}

func TestCatalogDatasetHandler_UnknownDataset(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/datasets/urn:dynamos:dataset:VU:nonexistent", nil)
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	req.SetPathValue("id", "urn:dynamos:dataset:VU:nonexistent")
	rec := httptest.NewRecorder()

	catalogDatasetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "CatalogError", ce.Type)
	assert.Equal(t, "not-found", ce.Code)
}

func TestCatalogDatasetHandler_MissingAuthorization(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/datasets/urn:dynamos:dataset:VU:wageGap", nil)
	req.SetPathValue("id", "urn:dynamos:dataset:VU:wageGap")
	rec := httptest.NewRecorder()

	catalogDatasetHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCatalogDatasetHandler_WrongMethod(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/datasets/urn:dynamos:dataset:VU:wageGap", nil)
	req.SetPathValue("id", "urn:dynamos:dataset:VU:wageGap")
	rec := httptest.NewRecorder()

	catalogDatasetHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestCatalogRequestHandler_MalformedJSON(t *testing.T) {
	startFixtureCatalogService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{not json"))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "invalid-request", ce.Code)
}

func TestCatalogRequestHandler_UpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"code": "internal-error", "error": "etcd unavailable"})
	}))
	t.Cleanup(ts.Close)
	catalogServiceURL = ts.URL

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", testAuthHeader("jorrit.stutterheim@cloudnation.nl"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "upstream-error", ce.Code)
}

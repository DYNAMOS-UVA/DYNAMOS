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

func setTestCatalogConfig(t *testing.T) {
	t.Helper()
	cfg, err := catalog.LoadConfig("config/example-catalog.json")
	require.NoError(t, err)
	catalogConfig = cfg
}

func TestCatalogRequestHandler_KnownParticipant(t *testing.T) {
	setTestCatalogConfig(t)

	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"CatalogRequestMessage","filter":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
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
	setTestCatalogConfig(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", nil)
	req.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCatalogRequestHandler_UnknownParticipant(t *testing.T) {
	setTestCatalogConfig(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "nobody@example.com")
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "CatalogError", ce.Type)
	assert.Equal(t, "not-provisioned", ce.Code)
}

func TestCatalogRequestHandler_MissingAuthorization(t *testing.T) {
	setTestCatalogConfig(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{}"))
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "missing-authorization", ce.Code)
}

func TestCatalogRequestHandler_WrongMethod(t *testing.T) {
	setTestCatalogConfig(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/request", nil)
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

func TestCatalogRequestHandler_MalformedJSON(t *testing.T) {
	setTestCatalogConfig(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/request", bytes.NewBufferString("{not json"))
	req.Header.Set("Authorization", "jorrit.stutterheim@cloudnation.nl")
	rec := httptest.NewRecorder()

	catalogRequestHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var ce catalogError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ce))
	assert.Equal(t, "invalid-request", ce.Code)
}

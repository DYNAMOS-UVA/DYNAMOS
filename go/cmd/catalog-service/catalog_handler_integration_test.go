//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHandlerTestData wires the package-level etcdClient/party (normally set
// in main()) and seeds one party's agreement + dataset, same as
// catalog_source_integration_test.go.
func seedHandlerTestData(t *testing.T) {
	t.Helper()

	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23791"
	}
	etcdClient = etcd.GetEtcdClient(endpoint)
	party = "VU"

	agreement := api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"jorrit.stutterheim@cloudnation.nl": {
				ID:                      "GUID",
				RequestTypes:            []string{"sqlDataRequest"},
				DataSets:                []string{"wageGap"},
				AllowedArchetypes:       []string{"computeToData"},
				AllowedComputeProviders: []string{"SURF"},
			},
		},
	}
	require.NoError(t, etcd.SaveStructToEtcd(etcdClient, "/policyEnforcer/agreements/VU", agreement))

	dataset := pb.Dataset{Name: "wageGap", Type: "csv", Delimiter: ";", Tables: []string{"Aanstellingen", "Personen"}}
	require.NoError(t, etcd.SaveStructToEtcd(etcdClient, "/datasets/wageGap", &dataset))
}

func TestInternalCatalogHandler_KnownParticipant(t *testing.T) {
	seedHandlerTestData(t)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/catalog?participant=jorrit.stutterheim@cloudnation.nl", nil)
	rec := httptest.NewRecorder()

	internalCatalogHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var cat catalog.Catalog
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cat))
	assert.Equal(t, "urn:dynamos:catalog:VU:for-jorrit.stutterheim@cloudnation.nl", cat.ID)
	require.Len(t, cat.Dataset, 1)
}

func TestInternalCatalogHandler_UnknownParticipant(t *testing.T) {
	seedHandlerTestData(t)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/catalog?participant=nobody@example.com", nil)
	rec := httptest.NewRecorder()

	internalCatalogHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ie internalError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
	assert.Equal(t, "participant-not-found", ie.Code)
}

func TestInternalCatalogHandler_MissingParticipant(t *testing.T) {
	seedHandlerTestData(t)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/catalog", nil)
	rec := httptest.NewRecorder()

	internalCatalogHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInternalDatasetHandler_KnownDataset(t *testing.T) {
	seedHandlerTestData(t)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/catalog/datasets/urn:dynamos:dataset:VU:wageGap?participant=jorrit.stutterheim@cloudnation.nl", nil)
	req.SetPathValue("id", "urn:dynamos:dataset:VU:wageGap")
	rec := httptest.NewRecorder()

	internalDatasetHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var rd catalog.RootDataset
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rd))
	assert.Equal(t, "urn:dynamos:dataset:VU:wageGap", rd.ID)
	assert.Contains(t, rd.Context, "https://w3id.org/dspace/2025/1/context.jsonld")
}

func TestInternalDatasetHandler_UnknownDataset(t *testing.T) {
	seedHandlerTestData(t)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/catalog/datasets/urn:dynamos:dataset:VU:nonexistent?participant=jorrit.stutterheim@cloudnation.nl", nil)
	req.SetPathValue("id", "urn:dynamos:dataset:VU:nonexistent")
	rec := httptest.NewRecorder()

	internalDatasetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ie internalError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
	assert.Equal(t, "dataset-not-found", ie.Code)
}

func TestInternalDatasetHandler_UnknownParticipant(t *testing.T) {
	seedHandlerTestData(t)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/catalog/datasets/urn:dynamos:dataset:VU:wageGap?participant=nobody@example.com", nil)
	req.SetPathValue("id", "urn:dynamos:dataset:VU:wageGap")
	rec := httptest.NewRecorder()

	internalDatasetHandler(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var ie internalError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
	assert.Equal(t, "participant-not-found", ie.Code)
}

package main

import (
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAgreement() *api.Agreement {
	return &api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"jorrit.stutterheim@cloudnation.nl": {
				ID:                      "GUID",
				RequestTypes:            []string{"sqlDataRequest", "genericRequest"},
				DataSets:                []string{"wageGap"},
				AllowedArchetypes:       []string{"computeToData", "dataThroughTtp"},
				AllowedComputeProviders: []string{"SURF"},
			},
		},
	}
}

func testDatasets() map[string]*pb.Dataset {
	return map[string]*pb.Dataset{
		"wageGap": {Name: "wageGap", Type: "csv", Delimiter: ";", Tables: []string{"Aanstellingen", "Personen"}},
	}
}

func TestBuildConfig(t *testing.T) {
	cfg, err := buildConfig("VU", testAgreement(), "jorrit.stutterheim@cloudnation.nl", testDatasets())
	require.NoError(t, err)

	assert.Equal(t, "VU", cfg.Party)
	assert.Equal(t, "http://vu.vu.svc.cluster.local:8080/agent/v1/sqlDataRequest/vu", cfg.AgentEndpoint)

	require.Len(t, cfg.Datasets, 1)
	assert.Equal(t, "wageGap", cfg.Datasets[0].Name)
	assert.Equal(t, []string{"Aanstellingen", "Personen"}, cfg.Datasets[0].Tables)

	require.Contains(t, cfg.Relations, "jorrit.stutterheim@cloudnation.nl")
	assert.Equal(t, "GUID", cfg.Relations["jorrit.stutterheim@cloudnation.nl"].ID)
}

func TestBuildConfig_UnknownParticipant(t *testing.T) {
	_, err := buildConfig("VU", testAgreement(), "nobody@example.com", testDatasets())
	assert.Error(t, err)
}

func TestBuildConfig_MissingDatasetReference(t *testing.T) {
	_, err := buildConfig("VU", testAgreement(), "jorrit.stutterheim@cloudnation.nl", map[string]*pb.Dataset{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wageGap")
}

func TestBuildConfig_OnlyReferencedDatasetsIncluded(t *testing.T) {
	datasets := testDatasets()
	datasets["other"] = &pb.Dataset{Name: "other", Type: "csv", Tables: []string{"Unrelated"}}

	cfg, err := buildConfig("VU", testAgreement(), "jorrit.stutterheim@cloudnation.nl", datasets)
	require.NoError(t, err)

	require.Len(t, cfg.Datasets, 1)
	assert.Equal(t, "wageGap", cfg.Datasets[0].Name)
}

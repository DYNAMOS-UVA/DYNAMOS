package catalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig() *Config {
	return &Config{
		Party:         "VU",
		AgentEndpoint: "https://vu-agent.vu.svc.cluster.local/agent/v1/sqlDataRequest/vu-agent",
		Datasets: []DatasetConfig{
			{Name: "wageGap", Type: "csv", Delimiter: ";", Tables: []string{"Aanstellingen", "Personen"}},
		},
		Relations: map[string]RelationConfig{
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

func TestBuildCatalog(t *testing.T) {
	cat, err := BuildCatalog(testConfig(), "jorrit.stutterheim@cloudnation.nl")
	require.NoError(t, err)

	assert.Equal(t, "Catalog", cat.Type)
	assert.Equal(t, "urn:dynamos:party:VU", cat.ParticipantID)
	assert.Equal(t, "urn:dynamos:catalog:VU:for-jorrit.stutterheim@cloudnation.nl", cat.ID)

	require.Len(t, cat.Service, 1)
	assert.Equal(t, "urn:dynamos:service:vu-agent", cat.Service[0].ID)
	assert.Equal(t, "https://vu-agent.vu.svc.cluster.local/agent/v1/sqlDataRequest/vu-agent", cat.Service[0].EndpointURL)

	require.Len(t, cat.Dataset, 1)
	ds := cat.Dataset[0]
	assert.Equal(t, "urn:dynamos:dataset:VU:wageGap", ds.ID)

	// One Distribution per table (decision 1 in dynamos-catalog-schema.md).
	require.Len(t, ds.Distribution, 2)
	assert.Equal(t, "Aanstellingen", ds.Distribution[0].Table)
	assert.Equal(t, "Personen", ds.Distribution[1].Table)
	for _, d := range ds.Distribution {
		assert.Equal(t, "dynamos:sqlDataRequest", d.Format)
		assert.Equal(t, "urn:dynamos:service:vu-agent", d.AccessService)
		assert.Equal(t, ";", d.Delimiter)
	}

	require.Len(t, ds.HasPolicy, 1)
	offer := ds.HasPolicy[0]
	assert.Equal(t, "urn:dynamos:offer:VU:GUID", offer.ID)
	assert.Equal(t, "urn:dynamos:party:VU", offer.Assigner)
	assert.Equal(t, "mailto:jorrit.stutterheim@cloudnation.nl", offer.Assignee)

	// One permission per RequestTypes entry (decision 5).
	require.Len(t, offer.Permission, 2)
	assert.Equal(t, "dynamos:sqlDataRequest", offer.Permission[0].Action)
	assert.Equal(t, "dynamos:genericRequest", offer.Permission[1].Action)
	for _, p := range offer.Permission {
		require.Len(t, p.Constraint, 2)
		assert.Equal(t, "dynamos:archetype", p.Constraint[0].LeftOperand)
		assert.Equal(t, "isAnyOf", p.Constraint[0].Operator)
		assert.Equal(t, []string{"computeToData", "dataThroughTtp"}, p.Constraint[0].RightOperand)
		assert.Equal(t, "dynamos:computeProvider", p.Constraint[1].LeftOperand)
		assert.Equal(t, []string{"SURF"}, p.Constraint[1].RightOperand)
	}

	// sensitive_columns has no field to even populate - decision 3 is enforced
	// by DatasetConfig's shape, not by anything at build time. Nothing to assert
	// here beyond DatasetConfig not compiling with such a field.
}

func TestBuildCatalog_UnknownParticipant(t *testing.T) {
	_, err := BuildCatalog(testConfig(), "nobody@example.com")
	assert.Error(t, err)
}

func TestBuildCatalog_MarshalsToValidJSONLD(t *testing.T) {
	cat, err := BuildCatalog(testConfig(), "jorrit.stutterheim@cloudnation.nl")
	require.NoError(t, err)

	data, err := json.MarshalIndent(cat, "", "  ")
	require.NoError(t, err)

	var generic map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &generic))

	assert.Contains(t, generic, "@context")
	assert.Equal(t, "Catalog", generic["@type"])
	assert.Contains(t, generic, "participantId")
}

package main

import (
	"encoding/json"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureAgreement matches docs/catalog/dynamos-catalog-example.jsonld's
// Offer permission/constraint shape, the worked example this codebase's
// negotiation docs already build on.
const fixtureAgreement = `{
	"@id": "urn:dynamos:agreement:VU:GUID",
	"target": "urn:dynamos:dataset:VU:wageGap",
	"assigner": "urn:dynamos:party:VU",
	"assignee": "mailto:jorrit.stutterheim@cloudnation.nl",
	"permission": [
		{
			"action": "dynamos:sqlDataRequest",
			"constraint": [
				{"leftOperand": "dynamos:archetype", "operator": "isAnyOf", "rightOperand": ["computeToData", "dataThroughTtp"]},
				{"leftOperand": "dynamos:computeProvider", "operator": "isAnyOf", "rightOperand": ["SURF"]}
			]
		},
		{
			"action": "dynamos:genericRequest",
			"constraint": [
				{"leftOperand": "dynamos:archetype", "operator": "isAnyOf", "rightOperand": ["computeToData", "dataThroughTtp"]},
				{"leftOperand": "dynamos:computeProvider", "operator": "isAnyOf", "rightOperand": ["SURF"]}
			]
		}
	]
}`

func TestDeriveRelation_MatchesCatalogExample(t *testing.T) {
	rel, err := deriveRelation("urn:dynamos:negotiation:VU:GUID", json.RawMessage(fixtureAgreement))
	require.NoError(t, err)

	assert.Equal(t, api.Relation{
		ID:                      "urn:dynamos:negotiation:VU:GUID",
		RequestTypes:            []string{"sqlDataRequest", "genericRequest"},
		DataSets:                []string{"wageGap"},
		AllowedArchetypes:       []string{"computeToData", "dataThroughTtp"},
		AllowedComputeProviders: []string{"SURF"},
	}, rel)
}

func TestDeriveRelation_DedupesAcrossPermissions(t *testing.T) {
	// Both permissions above name the identical archetype/computeProvider
	// constraints - a naive concat would double every value.
	rel, err := deriveRelation("id-1", json.RawMessage(fixtureAgreement))
	require.NoError(t, err)

	assert.Len(t, rel.AllowedArchetypes, 2)
	assert.Len(t, rel.AllowedComputeProviders, 1)
}

func TestDeriveRelation_NoTarget(t *testing.T) {
	rel, err := deriveRelation("id-1", json.RawMessage(`{"permission":[]}`))
	require.NoError(t, err)

	assert.Empty(t, rel.DataSets)
	assert.Equal(t, "id-1", rel.ID)
}

func TestDeriveRelation_UnknownConstraintIgnored(t *testing.T) {
	body := `{"target":"urn:dynamos:dataset:VU:wageGap","permission":[{"action":"dynamos:sqlDataRequest","constraint":[{"leftOperand":"dynamos:somethingElse","operator":"isAnyOf","rightOperand":["x"]}]}]}`
	rel, err := deriveRelation("id-1", json.RawMessage(body))
	require.NoError(t, err)

	assert.Empty(t, rel.AllowedArchetypes)
	assert.Empty(t, rel.AllowedComputeProviders)
	assert.Equal(t, []string{"sqlDataRequest"}, rel.RequestTypes)
}

func TestDeriveRelation_MalformedJSON(t *testing.T) {
	_, err := deriveRelation("id-1", json.RawMessage(`{not json`))
	require.Error(t, err)
}

func TestUrnLastSegment(t *testing.T) {
	assert.Equal(t, "wageGap", urnLastSegment("urn:dynamos:dataset:VU:wageGap"))
	assert.Equal(t, "plain", urnLastSegment("plain"))
}

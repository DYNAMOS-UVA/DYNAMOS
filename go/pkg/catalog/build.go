package catalog

import (
	"fmt"
	"strings"
)

// datasetsForParticipant resolves the party/service identifiers and builds
// every Dataset visible to participantEmail's Relation. Shared by
// BuildCatalog and BuildDataset - the DSP Catalog Protocol's two required
// message types (Catalog Request and Dataset Request) - so both agree on
// what a participant can see and how each Dataset is shaped.
func datasetsForParticipant(cfg *Config, participantEmail string) (relation RelationConfig, partyURN, serviceID string, datasets []Dataset, err error) {
	relation, ok := cfg.Relations[participantEmail]
	if !ok {
		return RelationConfig{}, "", "", nil, fmt.Errorf("no relation found for participant %q", participantEmail)
	}

	partyURN = fmt.Sprintf("urn:dynamos:party:%s", cfg.Party)
	serviceID = fmt.Sprintf("urn:dynamos:service:%s-agent", strings.ToLower(cfg.Party))

	datasetByName := make(map[string]DatasetConfig, len(cfg.Datasets))
	for _, d := range cfg.Datasets {
		datasetByName[d.Name] = d
	}

	datasets = make([]Dataset, 0, len(relation.DataSets))
	for _, dsName := range relation.DataSets {
		dsCfg, ok := datasetByName[dsName]
		if !ok {
			return RelationConfig{}, "", "", nil, fmt.Errorf("relation %q references unknown dataset %q", relation.ID, dsName)
		}
		datasets = append(datasets, buildDataset(cfg.Party, dsCfg, relation, serviceID, partyURN, participantEmail))
	}

	return relation, partyURN, serviceID, datasets, nil
}

// BuildCatalog builds a DSP Catalog scoped to a single requesting participant,
// per decision 6 in docs/catalog/dynamos-catalog-schema.md: since a DYNAMOS
// Relation is already keyed per counterparty, and the DSP spec allows a
// Catalog to be dynamically generated per requester's credentials, the
// catalog endpoint generates one Catalog per participant rather than a
// single global catalog for the party. Returns an error if no Relation
// exists for that participant (they have no visible datasets).
func BuildCatalog(cfg *Config, participantEmail string) (*Catalog, error) {
	_, partyURN, serviceID, datasets, err := datasetsForParticipant(cfg, participantEmail)
	if err != nil {
		return nil, err
	}

	return &Catalog{
		Context:       Context,
		ID:            fmt.Sprintf("urn:dynamos:catalog:%s:for-%s", cfg.Party, participantEmail),
		Type:          "Catalog",
		ParticipantID: partyURN,
		Service: []DataService{
			{ID: serviceID, Type: "DataService", EndpointURL: cfg.AgentEndpoint},
		},
		Dataset: datasets,
	}, nil
}

// BuildDataset builds a single Dataset scoped to participantEmail, backing
// the DSP Dataset Request endpoint (GET /catalog/datasets/:id) - the Catalog
// Protocol's second required message type alongside Catalog Request
// (docs/catalog/spec-reference/specifications/catalog.protocol.md). Returns
// an error if the participant has no Relation, or if datasetID doesn't match
// one of the datasets visible to them.
func BuildDataset(cfg *Config, participantEmail, datasetID string) (*Dataset, error) {
	_, _, _, datasets, err := datasetsForParticipant(cfg, participantEmail)
	if err != nil {
		return nil, err
	}

	for _, ds := range datasets {
		if ds.ID == datasetID {
			return &ds, nil
		}
	}

	return nil, fmt.Errorf("no dataset %q visible to participant %q", datasetID, participantEmail)
}

func buildDataset(party string, ds DatasetConfig, rel RelationConfig, serviceID, partyURN, assigneeEmail string) Dataset {
	constraints := []Constraint{
		{LeftOperand: "dynamos:archetype", Operator: "isAnyOf", RightOperand: rel.AllowedArchetypes},
		{LeftOperand: "dynamos:computeProvider", Operator: "isAnyOf", RightOperand: rel.AllowedComputeProviders},
	}

	permissions := make([]Permission, 0, len(rel.RequestTypes))
	for _, action := range rel.RequestTypes {
		permissions = append(permissions, Permission{
			Action:     "dynamos:" + action,
			Constraint: constraints,
		})
	}

	distributions := make([]Distribution, 0, len(ds.Tables))
	for _, table := range ds.Tables {
		distributions = append(distributions, Distribution{
			Type:          "Distribution",
			Format:        dynamosAccessFormat,
			AccessService: serviceID,
			Table:         table,
			Delimiter:     ds.Delimiter,
		})
	}

	return Dataset{
		ID:   fmt.Sprintf("urn:dynamos:dataset:%s:%s", party, ds.Name),
		Type: "Dataset",
		HasPolicy: []Offer{
			{
				ID:         fmt.Sprintf("urn:dynamos:offer:%s:%s", party, rel.ID),
				Type:       "Offer",
				Assigner:   partyURN,
				Assignee:   "mailto:" + assigneeEmail,
				Permission: permissions,
			},
		},
		Distribution: distributions,
	}
}

package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// ErrParticipantNotFound / ErrDatasetNotFound: sentinels so the internal API
// (issue #28) can tell a business "not found" (404) apart from an etcd I/O
// failure (500).
var (
	ErrParticipantNotFound = errors.New("no relation found for participant")
	ErrDatasetNotFound     = errors.New("dataset not visible to participant")
)

// buildConfig builds a *catalog.Config from already-fetched data - pure, so
// it's unit-testable with plain fixtures, no etcd needed.
func buildConfig(party string, agreement *api.Agreement, participantEmail string, datasets map[string]*pb.Dataset) (*catalog.Config, error) {
	relation, ok := agreement.Relations[participantEmail]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrParticipantNotFound, participantEmail)
	}

	dsConfigs := make([]catalog.DatasetConfig, 0, len(relation.DataSets))
	for _, name := range relation.DataSets {
		ds, ok := datasets[name]
		if !ok {
			return nil, fmt.Errorf("relation %q references unknown dataset %q", relation.ID, name)
		}
		dsConfigs = append(dsConfigs, catalog.DatasetConfig{
			Name:      ds.Name,
			Type:      ds.Type,
			Delimiter: ds.Delimiter,
			Tables:    ds.Tables,
		})
	}

	return &catalog.Config{
		Party:         party,
		AgentEndpoint: deriveAgentEndpoint(party),
		Datasets:      dsConfigs,
		Relations:     map[string]api.Relation{participantEmail: relation},
	}, nil
}

// fetchConfig reads party's agreement + only the referenced datasets from
// etcd, read-through per request (no cache - low-frequency discovery calls).
func fetchConfig(etcdClient *clientv3.Client, party, participantEmail string) (*catalog.Config, error) {
	var agreement api.Agreement
	if _, err := etcd.GetAndUnmarshalJSON(etcdClient, "/policyEnforcer/agreements/"+party, &agreement); err != nil {
		return nil, fmt.Errorf("fetching agreement for party %q: %w", party, err)
	}

	relation, ok := agreement.Relations[participantEmail]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrParticipantNotFound, participantEmail)
	}

	datasets := make(map[string]*pb.Dataset, len(relation.DataSets))
	for _, name := range relation.DataSets {
		var ds pb.Dataset
		if _, err := etcd.GetAndUnmarshalJSON(etcdClient, "/datasets/"+name, &ds); err != nil {
			return nil, fmt.Errorf("fetching dataset %q: %w", name, err)
		}
		datasets[name] = &ds
	}

	return buildConfig(party, &agreement, participantEmail, datasets)
}

// fetchDatasetConfig fetches only the one requested dataset, not every
// dataset the relation references - avoids O(n) etcd reads per request.
func fetchDatasetConfig(etcdClient *clientv3.Client, party, participantEmail, datasetID string) (*catalog.Config, error) {
	var agreement api.Agreement
	if _, err := etcd.GetAndUnmarshalJSON(etcdClient, "/policyEnforcer/agreements/"+party, &agreement); err != nil {
		return nil, fmt.Errorf("fetching agreement for party %q: %w", party, err)
	}

	relation, ok := agreement.Relations[participantEmail]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrParticipantNotFound, participantEmail)
	}

	name, ok := strings.CutPrefix(datasetID, fmt.Sprintf("urn:dynamos:dataset:%s:", party))
	if !ok || !slices.Contains(relation.DataSets, name) {
		return nil, fmt.Errorf("%w: %q", ErrDatasetNotFound, datasetID)
	}

	var ds pb.Dataset
	if _, err := etcd.GetAndUnmarshalJSON(etcdClient, "/datasets/"+name, &ds); err != nil {
		return nil, fmt.Errorf("fetching dataset %q: %w", name, err)
	}

	return buildConfig(party, &agreement, participantEmail, map[string]*pb.Dataset{name: &ds})
}

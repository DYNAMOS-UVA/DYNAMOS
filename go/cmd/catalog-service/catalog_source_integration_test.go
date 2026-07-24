//go:build integration
// +build integration

package main

import (
	"os"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
	"github.com/stretchr/testify/require"
)

// TestFetchConfig_Integration exercises fetchConfig against a real etcd
// (docker run -p 23790:2379 quay.io/coreos/etcd:v3.5.1 ...), not a mock -
// no etcd-mocking precedent exists anywhere in this repo (see
// go/cmd/orchestrator/archetype_test.go's abandoned attempt).
func TestFetchConfig_Integration(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

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
	require.NoError(t, etcd.SaveStructToEtcd(client, "/policyEnforcer/agreements/VU", agreement))

	dataset := pb.Dataset{Name: "wageGap", Type: "csv", Delimiter: ";", Tables: []string{"Aanstellingen", "Personen"}}
	require.NoError(t, etcd.SaveStructToEtcd(client, "/datasets/wageGap", &dataset))

	cfg, err := fetchConfig(client, "VU", "jorrit.stutterheim@cloudnation.nl")
	require.NoError(t, err)

	require.Equal(t, "VU", cfg.Party)
	require.Len(t, cfg.Datasets, 1)
	require.Equal(t, "wageGap", cfg.Datasets[0].Name)
	require.Equal(t, []string{"Aanstellingen", "Personen"}, cfg.Datasets[0].Tables)
	require.Contains(t, cfg.Relations, "jorrit.stutterheim@cloudnation.nl")
}

func TestFetchConfig_Integration_UnknownParticipant(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

	_, err := fetchConfig(client, "VU", "nobody@example.com")
	require.Error(t, err)
}

// TestFetchDatasetConfig_Integration_MultiDatasetRelation is a regression
// test for a real bug T2.5's TCK work surfaced: fetchDatasetConfig only
// fetches the one requested dataset (by design, see its own doc comment),
// but buildConfig loops over every name in relation.DataSets expecting each
// to be present in the datasets map - invisible until a relation had more
// than one dataset, since every relation in this repo only ever had exactly
// one before now.
func TestFetchDatasetConfig_Integration_MultiDatasetRelation(t *testing.T) {
	endpoint := os.Getenv("TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:23790"
	}
	client := etcd.GetEtcdClient(endpoint)
	defer client.Close()

	agreement := api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"multi-dataset@example.com": {
				ID:                      "multi-guid",
				RequestTypes:            []string{"sqlDataRequest"},
				DataSets:                []string{"wageGap", "otherDataset"},
				AllowedArchetypes:       []string{"computeToData"},
				AllowedComputeProviders: []string{"SURF"},
			},
		},
	}
	require.NoError(t, etcd.SaveStructToEtcd(client, "/policyEnforcer/agreements/VU", agreement))

	dataset := pb.Dataset{Name: "wageGap", Type: "csv", Delimiter: ";", Tables: []string{"Aanstellingen", "Personen"}}
	require.NoError(t, etcd.SaveStructToEtcd(client, "/datasets/wageGap", &dataset))
	// otherDataset deliberately never written to etcd - fetchDatasetConfig
	// must not need it to satisfy a request for wageGap alone.

	cfg, err := fetchDatasetConfig(client, "VU", "multi-dataset@example.com", "urn:dynamos:dataset:VU:wageGap")
	require.NoError(t, err)

	require.Len(t, cfg.Datasets, 1)
	require.Equal(t, "wageGap", cfg.Datasets[0].Name)
}

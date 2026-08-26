//go:build ignore

// ci_seed_baseline seeds the minimum etcd state a CI-run TCK needs before
// seed_cn_datasets.go can run: a wageGap dataset (CAT_01_01/01_02's own
// dataset id) and a baseline /policyEnforcer/agreements/VU document.
//
// Local dev gets this for free from configuration/etcd_launch_files/
// (agreements.json, datasets.json), loaded into a real etcd by
// cmd/orchestrator/etcd_config.go on every orchestrator start
// (registerPolicyEnforcerConfiguration) as part of dynamos-configuration.sh.
// CI runs a standalone etcd container instead of the full kind cluster, so
// nothing ever calls that path - this script is the CI substitute. It
// writes only the two keys the TCK groups actually read
// (writePolicyEnforcerRelation, cmd/negotiation-service/policy_enforcement.go,
// panics if /policyEnforcer/agreements/{party} doesn't exist at all yet),
// not the full agreements.json/datasets.json fixture set that dynamos'
// other, non-DSP microservices need.
//
// Run once, before seed_cn_datasets.go, against the CI etcd container.
//
// Usage: go run ci_seed_baseline.go
package main

import (
	"fmt"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
)

func main() {
	client := etcd.GetEtcdClient("http://localhost:2379")
	defer client.Close()

	wageGap := pb.Dataset{
		Name:      "wageGap",
		Type:      "csv",
		Delimiter: ";",
		Tables:    []string{"Aanstellingen", "Personen"},
	}
	if err := etcd.SaveStructToEtcd(client, "/datasets/wageGap", &wageGap); err != nil {
		panic(fmt.Errorf("writing /datasets/wageGap: %w", err))
	}

	agreement := api.Agreement{
		Name:             "VU",
		Relations:        map[string]api.Relation{},
		ComputeProviders: []string{"SURF", "otherCompany"},
		Archetypes:       []string{"computeToData", "dataThroughTtp", "reproducableScience"},
	}
	if err := etcd.SaveStructToEtcd(client, "/policyEnforcer/agreements/VU", agreement); err != nil {
		panic(fmt.Errorf("writing /policyEnforcer/agreements/VU: %w", err))
	}

	fmt.Println("Seeded baseline wageGap dataset and VU policyEnforcer agreement.")
}

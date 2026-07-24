//go:build ignore

// seed_cn_datasets seeds 15 TCK-only dataset entries (one per CN provider
// test) into etcd, and adds them to the TCK fixture identity's
// (did:web:localhost%3A9999) Relation.DataSets list. Real DYNAMOS's
// Relations model ties exactly one offer id to one identity
// (urn:dynamos:offer:{party}:{relation.ID} - see pkg/catalog/build.go's
// buildDataset), so the CN tests can't be told apart by offer id alone -
// every dataset this identity can see carries the identical offer id. The
// dataset id (each test's own CN_<n>_DATASETID) is the one thing that CAN
// vary per test, so it's what tck_auto_responder.go keys its per-test
// script table on. All 15 reuse wageGap's real table/delimiter config -
// only the name differs, purely so each test's negotiation is
// distinguishable.
//
// Run once before a TCK run (after any dynamos-configuration.sh re-run,
// since that resets /policyEnforcer/agreements/VU to baseline - see
// run-tck.sh's own seeding step).
package main

import (
	"fmt"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
)

const tckFixtureDID = "did:web:localhost%3A9999"

var cnTestDatasets = []string{
	"cn0101", "cn0102", "cn0103", "cn0104",
	"cn0201", "cn0202", "cn0203", "cn0204", "cn0205", "cn0206", "cn0207",
	"cn0301", "cn0302", "cn0303", "cn0304",
}

func main() {
	client := etcd.GetEtcdClient("http://localhost:2379")
	defer client.Close()

	for _, name := range cnTestDatasets {
		ds := pb.Dataset{
			Name:      name,
			Type:      "csv",
			Delimiter: ";",
			Tables:    []string{"Aanstellingen", "Personen"},
		}
		if err := etcd.SaveStructToEtcd(client, "/datasets/"+name, &ds); err != nil {
			panic(fmt.Errorf("writing /datasets/%s: %w", name, err))
		}
	}

	var a api.Agreement
	if _, err := etcd.GetAndUnmarshalJSON(client, "/policyEnforcer/agreements/VU", &a); err != nil {
		panic(fmt.Errorf("reading /policyEnforcer/agreements/VU: %w", err))
	}
	if a.Relations == nil {
		a.Relations = map[string]api.Relation{}
	}
	rel := a.Relations[tckFixtureDID]
	rel.ID = "tck-fixture-relation"
	rel.RequestTypes = []string{"sqlDataRequest", "genericRequest"}
	rel.AllowedArchetypes = []string{"computeToData", "dataThroughTtp"}
	rel.AllowedComputeProviders = []string{"SURF"}
	// wageGap stays too - CAT_01_01/01_02 still need it.
	rel.DataSets = append([]string{"wageGap"}, cnTestDatasets...)
	a.Relations[tckFixtureDID] = rel

	if err := etcd.SaveStructToEtcd(client, "/policyEnforcer/agreements/VU", a); err != nil {
		panic(fmt.Errorf("writing /policyEnforcer/agreements/VU: %w", err))
	}

	fmt.Println("Seeded", len(cnTestDatasets), "CN test datasets and updated the TCK fixture identity's Relation.")
}

package main

import (
	"fmt"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/lib"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
)

// mergeAgreementRelations returns fresh (the just-loaded static config for
// one party) with any Relations entries existing (whatever's currently in
// etcd for that party) has that fresh doesn't define. Static config is
// always authoritative for a key it does define - a relation present in
// both keeps fresh's value, unchanged from the old blind-put behavior.
// Relations that only exist in etcd (e.g. written by negotiation-service on
// a completed DSP negotiation) are carried over instead of being dropped.
func mergeAgreementRelations(fresh, existing api.Agreement) api.Agreement {
	for email, rel := range existing.Relations {
		if _, staticallyDefined := fresh.Relations[email]; !staticallyDefined {
			if fresh.Relations == nil {
				fresh.Relations = make(map[string]api.Relation)
			}
			fresh.Relations[email] = rel
		}
	}
	return fresh
}

func registerPolicyEnforcerConfiguration() {
	logger.Debug("Start registerPolicyEnforcerConfiguration")
	// Load request types
	var requestsTypes []api.RequestType
	lib.UnmarshalJsonFile(requestTypeConfigLocation, &requestsTypes)

	for _, requestType := range requestsTypes {
		etcd.SaveStructToEtcd[api.RequestType](etcdClient, fmt.Sprintf("/requestTypes/%s", requestType.Name), requestType)
	}

	// Load archetypes
	var archeTypes []api.Archetype
	lib.UnmarshalJsonFile(archetypeConfigLocation, &archeTypes)

	for _, archeType := range archeTypes {
		etcd.SaveStructToEtcd[api.Archetype](etcdClient, fmt.Sprintf("/archetypes/%s", archeType.Name), archeType)
	}

	// Load labels and allowedOutputs (microservice.json)
	var microservices []api.MicroserviceMetadata

	lib.UnmarshalJsonFile(microserviceMetadataConfigLocation, &microservices)

	for _, microservice := range microservices {
		etcd.SaveStructToEtcd[api.MicroserviceMetadata](etcdClient, fmt.Sprintf("/microservices/%s/chainMetadata", microservice.Name), microservice)
	}

	// Load agreemnents  (agreemnents.json) - merged into whatever's already at
	// the key, not blindly overwritten. negotiation-service (T2.4) also
	// writes into this same key when a DSP contract negotiation reaches
	// FINALIZED (Relations[consumerEmail]); a blind put here would silently
	// wipe those out every time this function runs (every orchestrator
	// start, or the updateEtc endpoint). Static config stays fully
	// authoritative for every field/relation it defines - the result is
	// byte-identical to the old blind put for anything agreements.json
	// mentions - the only change is that a Relations entry present in etcd
	// but absent from the static file is now kept instead of dropped.
	var agreements []api.Agreement

	lib.UnmarshalJsonFile(agreementsConfigLocation, &agreements)

	for _, agreement := range agreements {
		key := fmt.Sprintf("/policyEnforcer/agreements/%s", agreement.Name)

		var existing api.Agreement
		if _, err := etcd.GetAndUnmarshalJSON(etcdClient, key, &existing); err != nil {
			logger.Sugar().Errorw("failed to read existing policyEnforcer agreement, reseeding without merge", "party", agreement.Name, "error", err)
		} else {
			agreement = mergeAgreementRelations(agreement, existing)
		}

		etcd.SaveStructToEtcd[api.Agreement](etcdClient, key, agreement)
	}

	// Load agreemnents  (agreemnents.json)
	var datasets []*pb.Dataset

	lib.UnmarshalJsonFile(dataSetConfigLocation, &datasets)

	for _, dataset := range datasets {
		etcd.SaveStructToEtcd[*pb.Dataset](etcdClient, fmt.Sprintf("/datasets/%s", dataset.Name), dataset)
	}

	// Load   optional_microservices.json
	var optionalServices []api.OptionalServices

	lib.UnmarshalJsonFile(optionalMSConfigLocation, &optionalServices)

	for _, services := range optionalServices {
		for k, msList := range services.Types {
			for _, ms := range msList {
				key := fmt.Sprintf("/agents/%s/requestType/%s/%s ", services.DataSteward, k, ms)
				etcd.PutValueToEtcd(etcdClient, key, ms)
			}
		}
	}

}

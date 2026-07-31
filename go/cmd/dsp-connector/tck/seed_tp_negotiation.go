//go:build ignore

// seed_tp_negotiation seeds one already-FINALIZED negotiation directly into
// negotiation-service's etcd, owned by the TCK fixture identity
// (did:web:localhost%3A9999). T3.4's TP-group tests all reuse this single
// negotiation's providerPid as their agreementId (TP_XX_AGREEMENTID in
// tck.properties): unlike the CN group, no TP test needs its own dataset or
// scripted provider response - dsp-connector's provider endpoints already
// implement the transfer state machine deterministically, so one fixture
// agreement covers every test.
//
// negotiation-service's own Negotiation struct lives in cmd/negotiation-service
// (not a shared pkg), so this script mirrors its JSON shape locally rather
// than importing it.
//
// Run once before a TP-group TCK run (after any dynamos-configuration.sh
// re-run, which resets etcd to baseline):
//
//	go run seed_tp_negotiation.go
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
)

const tpTckFixtureDID = "did:web:localhost%3A9999"

// tpFixtureAgreementId is reused as every TP_XX_AGREEMENTID value in
// tck.properties.
const tpFixtureAgreementId = "urn:dynamos:negotiation:VU:tck-tp-fixture"

// negotiationSeed mirrors negotiation-service's own Negotiation struct
// (cmd/negotiation-service/negotiation.go) field-for-field.
type negotiationSeed struct {
	ProviderPid     string          `json:"providerPid"`
	ConsumerPid     string          `json:"consumerPid"`
	Party           string          `json:"party"`
	Participant     string          `json:"participant"`
	State           string          `json:"state"`
	Offer           json.RawMessage `json:"offer,omitempty"`
	Agreement       json.RawMessage `json:"agreement,omitempty"`
	CallbackAddress string          `json:"callbackAddress,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

func main() {
	client := etcd.GetEtcdClient("http://localhost:2379")
	defer client.Close()

	now := time.Now().UTC()
	n := negotiationSeed{
		ProviderPid: tpFixtureAgreementId,
		ConsumerPid: "urn:dynamos:tck:consumer:tp-fixture",
		Party:       "VU",
		Participant: tpTckFixtureDID,
		State:       "FINALIZED",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := etcd.SaveStructToEtcd(client, "/dsp/negotiations/"+tpFixtureAgreementId, &n); err != nil {
		panic(fmt.Errorf("writing /dsp/negotiations/%s: %w", tpFixtureAgreementId, err))
	}

	fmt.Println("Seeded TP fixture negotiation", tpFixtureAgreementId, "for", tpTckFixtureDID)
}

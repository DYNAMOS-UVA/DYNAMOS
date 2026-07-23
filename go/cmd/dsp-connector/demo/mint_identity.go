//go:build ignore

// mint_identity generates a fresh DSP identity for the Postman demo chain
// (T2.7's DAT verification + T2.3/T2.4 negotiation): a new ES256 keypair, a
// did:web document for the already-deployed fixture-did pod
// (dsp-connector namespace - see wiki/devops/mvd-demo-dataspace-setup.md
// part 6), a long-lived signed DAT for it, and a matching Relation seeded
// into VU's real policyEnforcer agreement key (read-modify-write, not a
// blind overwrite - jorrit's existing relation must survive).
//
// Run via configuration/demo/mint-identity.sh, not directly - that script
// also republishes the ConfigMap and restarts the fixture-did pod, and
// port-forwards etcd first.
//
// Usage: go run mint_identity.go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/golang-jwt/jwt/v5"
)

const (
	fixtureDID = "did:web:fixture-did.dsp-connector.svc.cluster.local"
	party      = "VU"
	datasetID  = "wageGap"
)

func main() {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	doc := map[string]interface{}{
		"@context": []string{"https://www.w3.org/ns/did/v1"},
		"id":       fixtureDID,
		"verificationMethod": []map[string]interface{}{
			{
				"id":         fixtureDID + "#key-1",
				"type":       "JsonWebKey2020",
				"controller": fixtureDID,
				"publicKeyJwk": map[string]string{
					"kty": "EC",
					"crv": "P-256",
					"x":   base64.RawURLEncoding.EncodeToString(priv.X.Bytes()),
					"y":   base64.RawURLEncoding.EncodeToString(priv.Y.Bytes()),
				},
			},
		},
	}
	docJSON, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("/tmp/dsp-demo-did.json", docJSON, 0o644); err != nil {
		panic(err)
	}

	dat := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": fixtureDID,
	})
	datSigned, err := dat.SignedString(priv)
	if err != nil {
		panic(err)
	}

	client := etcd.GetEtcdClient("http://localhost:2379")
	defer client.Close()

	var a api.Agreement
	if _, err := etcd.GetAndUnmarshalJSON(client, "/policyEnforcer/agreements/"+party, &a); err != nil {
		panic(fmt.Errorf("reading /policyEnforcer/agreements/%s: %w", party, err))
	}
	if a.Relations == nil {
		a.Relations = map[string]api.Relation{}
	}
	a.Relations[fixtureDID] = api.Relation{
		ID:                      "demo-identity",
		RequestTypes:            []string{"sqlDataRequest", "genericRequest"},
		DataSets:                []string{datasetID},
		AllowedArchetypes:       []string{"computeToData", "dataThroughTtp"},
		AllowedComputeProviders: []string{"SURF"},
	}
	if err := etcd.SaveStructToEtcd(client, "/policyEnforcer/agreements/"+party, a); err != nil {
		panic(fmt.Errorf("writing /policyEnforcer/agreements/%s: %w", party, err))
	}

	fmt.Println("Wrote /tmp/dsp-demo-did.json")
	fmt.Println("Seeded /policyEnforcer/agreements/" + party + " (relation key: " + fixtureDID + ")")
	fmt.Println()
	fmt.Println("DID:")
	fmt.Println(fixtureDID)
	fmt.Println()
	fmt.Println("DAT:")
	fmt.Println(datSigned)
}

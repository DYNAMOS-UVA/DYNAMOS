//go:build ignore

// gen_fixture generates the TCK harness's fixture DID document + a
// long-lived signed DAT for it (issue #56's DAT verification needs a real
// signed token, not a static email string, in tck.properties). Run once,
// commit the outputs (fixture/.well-known/did.json + the token embedded in
// tck.properties); the private key is never written to disk or committed -
// only its public half (in the DID document) and its signature (baked into
// the already-signed token) are needed afterward.
//
// Usage: go run gen_fixture.go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/golang-jwt/jwt/v5"
)

// fixtureDID is the identity this fixture proves - the whole point of a DAT
// (see dat_verification.go) is that the verified participant *is* this DID,
// not a claim read out of it. Whoever seeds real etcd data for a live TCK
// CAT-group run (a separate, pre-existing requirement - catalog-service
// reads etcd, not config/example-catalog.json, since T1.4's migration off
// static config) needs a Relations entry keyed by exactly this string.
const fixtureDID = "did:web:localhost%3A9999"

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
	if err := os.WriteFile("fixture/.well-known/did.json", docJSON, 0o644); err != nil {
		panic(err)
	}

	dat := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": fixtureDID,
		// deliberately no "exp" - this fixture token needs to keep working
		// indefinitely without regeneration.
	})
	datSigned, err := dat.SignedString(priv)
	if err != nil {
		panic(err)
	}

	fmt.Println("Wrote fixture/.well-known/did.json")
	fmt.Println()
	fmt.Println("Fixture DAT (paste into tck.properties'")
	fmt.Println("dataspacetck.dsp.connector.http.headers.authorization):")
	fmt.Println()
	fmt.Println(datSigned)
}

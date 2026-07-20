package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// Shared DAT test fixtures for catalog_handler_test.go / negotiation_handler_test.go:
// those tests only care that a given email is (or isn't) the verified
// participant, not about DAT verification itself (dat_verification_test.go
// owns that) - so TestMain wires a single fixture key/DID for the whole
// package's test binary, and testAuthHeader mints a real signed token for
// whatever email a test needs, replacing what used to be a raw email string
// set directly as the Authorization header.

const testFixtureDID = "did:web:test.dynamos.local:fixture"

var testFixtureKey *ecdsa.PrivateKey

func TestMain(m *testing.M) {
	var err error
	testFixtureKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	datResolver = func(did string) (crypto.PublicKey, error) {
		if did != testFixtureDID {
			return nil, ErrDATInvalid
		}
		return &testFixtureKey.PublicKey, nil
	}
	os.Exit(m.Run())
}

// testAuthHeader mints a real, verifiable DAT for the given email - the
// Authorization header value every fixture-based handler test needs now
// that participantFromRequest performs real verification (issue #56)
// instead of trusting a raw string.
func testAuthHeader(email string) string {
	dsc := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"vc": map[string]interface{}{
			"type":              dataStewardCredentialType,
			"credentialSubject": map[string]interface{}{"email": email},
		},
	})
	dscSigned, err := dsc.SignedString([]byte("unused-in-tests"))
	if err != nil {
		panic(err)
	}

	dat := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss":         testFixtureDID,
		"credentials": []interface{}{dscSigned},
	})
	signed, err := dat.SignedString(testFixtureKey)
	if err != nil {
		panic(err)
	}
	return signed
}

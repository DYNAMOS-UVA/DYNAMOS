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
// those tests only care that a given identity string is (or isn't) the
// verified participant, not about DAT verification itself
// (dat_verification_test.go owns that, including DID-specific resolver
// behavior) - so TestMain wires one fixture key for the whole package's
// test binary (accepting any claimed DID, since these tests aren't
// exercising DID resolution), and testAuthHeader mints a real signed token
// asserting whatever identity a test needs as its "iss", replacing what
// used to be a raw string set directly as the Authorization header.

var testFixtureKey *ecdsa.PrivateKey

func TestMain(m *testing.M) {
	var err error
	testFixtureKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	datResolver = func(did string) (crypto.PublicKey, error) {
		return &testFixtureKey.PublicKey, nil
	}
	os.Exit(m.Run())
}

// testAuthHeader mints a real, verifiable DAT asserting the given identity
// as its holder DID - the Authorization header value every fixture-based
// handler test needs now that participantFromRequest performs real
// verification (issue #56) instead of trusting a raw string. The identity
// is whatever the verified participant should be - a real DID in
// production, but these unit tests don't care about DID shape, only that
// participantFromRequest's return value matches what was asserted.
func testAuthHeader(identity string) string {
	dat := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": identity,
	})
	signed, err := dat.SignedString(testFixtureKey)
	if err != nil {
		panic(err)
	}
	return signed
}

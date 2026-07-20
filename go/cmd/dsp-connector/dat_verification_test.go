package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

const testHolderDID = "did:web:identityhub.consumer.svc.cluster.local%3A7083:alice"

func credentialJWT(t *testing.T, vcType string, subject map[string]interface{}) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"vc": map[string]interface{}{
			"type":              vcType,
			"credentialSubject": subject,
		},
	})
	// Embedded credentials are read for claims only (see file doc comment) -
	// any key works here, signature is never checked.
	signed, err := tok.SignedString([]byte("unused-in-this-test"))
	if err != nil {
		t.Fatalf("signing fixture credential: %v", err)
	}
	return signed
}

func datToken(t *testing.T, method jwt.SigningMethod, key crypto.PrivateKey, iss string, credentials []interface{}) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, jwt.MapClaims{
		"iss":         iss,
		"credentials": credentials,
	})
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("signing fixture DAT: %v", err)
	}
	return signed
}

func withFixtureResolver(t *testing.T, did string, pub crypto.PublicKey) {
	t.Helper()
	original := datResolver
	datResolver = func(gotDID string) (crypto.PublicKey, error) {
		if gotDID != did {
			return nil, ErrDATInvalid
		}
		return pub, nil
	}
	t.Cleanup(func() { datResolver = original })
}

func TestVerifyDAT_ValidToken_ES256_ReturnsEmail(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	withFixtureResolver(t, testHolderDID, &priv.PublicKey)

	dsc := credentialJWT(t, dataStewardCredentialType, map[string]interface{}{"email": "alice@example.com"})
	token := datToken(t, jwt.SigningMethodES256, priv, testHolderDID, []interface{}{dsc})

	email, err := verifyDAT(token)
	if err != nil {
		t.Fatalf("verifyDAT: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("got email %q, want alice@example.com", email)
	}
}

func TestVerifyDAT_ValidToken_EdDSA_ReturnsEmail(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	withFixtureResolver(t, testHolderDID, pub)

	dsc := credentialJWT(t, dataStewardCredentialType, map[string]interface{}{"email": "alice@example.com"})
	token := datToken(t, jwt.SigningMethodEdDSA, priv, testHolderDID, []interface{}{dsc})

	email, err := verifyDAT(token)
	if err != nil {
		t.Fatalf("verifyDAT: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("got email %q, want alice@example.com", email)
	}
}

func TestVerifyDAT_MultipleCredentials_FindsDataSteward(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	withFixtureResolver(t, testHolderDID, &priv.PublicKey)

	membership := credentialJWT(t, "MembershipCredential", map[string]interface{}{"membership": "active"})
	dsc := credentialJWT(t, dataStewardCredentialType, map[string]interface{}{"email": "alice@example.com"})
	token := datToken(t, jwt.SigningMethodES256, priv, testHolderDID, []interface{}{membership, dsc})

	email, err := verifyDAT(token)
	if err != nil {
		t.Fatalf("verifyDAT: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("got email %q, want alice@example.com", email)
	}
}

func TestVerifyDAT_WrongSigningKey_Fails(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Resolver returns a different key than the one that actually signed -
	// simulates a forged/replayed token.
	withFixtureResolver(t, testHolderDID, &otherPriv.PublicKey)

	dsc := credentialJWT(t, dataStewardCredentialType, map[string]interface{}{"email": "alice@example.com"})
	token := datToken(t, jwt.SigningMethodES256, priv, testHolderDID, []interface{}{dsc})

	if _, err := verifyDAT(token); err == nil {
		t.Fatal("expected verification failure, got nil error")
	}
}

func TestVerifyDAT_NoDataStewardCredential_Fails(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	withFixtureResolver(t, testHolderDID, &priv.PublicKey)

	manufacturer := credentialJWT(t, "ManufacturerCredential", map[string]interface{}{"part_types": "non_critical"})
	token := datToken(t, jwt.SigningMethodES256, priv, testHolderDID, []interface{}{manufacturer})

	if _, err := verifyDAT(token); err == nil {
		t.Fatal("expected failure (no DataStewardCredential present), got nil error")
	}
}

func TestVerifyDAT_MissingIssuerClaim_Fails(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"credentials": []interface{}{},
	})
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := verifyDAT(signed); err == nil {
		t.Fatal("expected failure (missing iss claim), got nil error")
	}
}

func TestVerifyDAT_MalformedToken_Fails(t *testing.T) {
	if _, err := verifyDAT("not-a-real-jwt"); err == nil {
		t.Fatal("expected failure on malformed token, got nil error")
	}
}

func TestDIDWebDocumentURL(t *testing.T) {
	cases := []struct {
		name    string
		did     string
		want    string
		wantErr bool
	}{
		{
			name: "host and one path segment (matches MVD's own seeded participant DIDs)",
			did:  "did:web:identityhub.consumer.svc.cluster.local%3A7083:consumer",
			want: "https://identityhub.consumer.svc.cluster.local:7083/consumer/did.json",
		},
		{
			name: "host only, no path segments",
			did:  "did:web:example.com",
			want: "https://example.com/.well-known/did.json",
		},
		{
			name:    "not a did:web DID",
			did:     "did:key:z6Mk...",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := didWebDocumentURL(tc.did)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJWKPublicKey_ECAndOKP(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecJWK := &jwk{
		Kty: "EC",
		Crv: "P-256",
		X:   base64URLEncode(priv.X.Bytes()),
		Y:   base64URLEncode(priv.Y.Bytes()),
	}
	ecPub, err := ecJWK.publicKey()
	if err != nil {
		t.Fatalf("EC publicKey(): %v", err)
	}
	if _, ok := ecPub.(*ecdsa.PublicKey); !ok {
		t.Errorf("EC publicKey() returned %T, want *ecdsa.PublicKey", ecPub)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	okpJWK := &jwk{
		Kty: "OKP",
		Crv: "Ed25519",
		X:   base64URLEncode(pub),
	}
	okpPub, err := okpJWK.publicKey()
	if err != nil {
		t.Fatalf("OKP publicKey(): %v", err)
	}
	if _, ok := okpPub.(ed25519.PublicKey); !ok {
		t.Errorf("OKP publicKey() returned %T, want ed25519.PublicKey", okpPub)
	}
}

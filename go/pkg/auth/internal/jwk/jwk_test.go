package jwk

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(t *testing.T, bits int, kid string) (Key, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	return Key{Kty: "RSA", Kid: kid, N: b64(priv.N.Bytes()), E: b64(big.NewInt(int64(priv.E)).Bytes())}, priv
}

func TestPublicKeyRSA(t *testing.T) {
	k, priv := rsaJWK(t, 2048, "r1")
	pub, err := k.PublicKey()
	require.NoError(t, err)
	assert.True(t, priv.PublicKey.Equal(pub))

	weak, _ := rsaJWK(t, 1024, "weak")
	_, err = weak.PublicKey()
	assert.ErrorIs(t, err, ErrUnusable)
}

func TestPublicKeyEC(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	k := Key{Kty: "EC", Crv: "P-256", X: b64(priv.X.Bytes()), Y: b64(priv.Y.Bytes())}
	pub, err := k.PublicKey()
	require.NoError(t, err)
	assert.True(t, priv.PublicKey.Equal(pub))

	k.Y = b64([]byte{1, 2, 3})
	_, err = k.PublicKey()
	assert.Error(t, err, "point not on curve")

	_, err = Key{Kty: "EC", Crv: "secp256k1", X: "AA", Y: "AA"}.PublicKey()
	assert.ErrorIs(t, err, ErrUnusable)
}

func TestPublicKeyEd25519AndThumbprint(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	k := FromEd25519(pub)
	got, err := k.PublicKey()
	require.NoError(t, err)
	assert.True(t, pub.Equal(got))
	assert.Equal(t, ThumbprintEd25519(pub), k.Kid)

	_, err = Key{Kty: "OKP", Crv: "Ed25519", X: b64([]byte{1})}.PublicKey()
	assert.Error(t, err)
}

func TestThumbprintRFC8037Vector(t *testing.T) {
	// RFC 8037 appendix A.3.
	x, err := base64.RawURLEncoding.DecodeString("11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo")
	require.NoError(t, err)
	assert.Equal(t, "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k", ThumbprintEd25519(ed25519.PublicKey(x)))
}

func TestPublicKeyRejects(t *testing.T) {
	_, err := Key{Kty: "oct"}.PublicKey()
	assert.ErrorIs(t, err, ErrUnusable)
	_, err = Key{Kty: "OKP", Crv: "Ed25519", Use: "enc"}.PublicKey()
	assert.ErrorIs(t, err, ErrUnusable)
	_, err = Key{Kty: "RSA", N: "", E: "AQAB"}.PublicKey()
	assert.Error(t, err)
}

func TestParseSet(t *testing.T) {
	r, _ := rsaJWK(t, 2048, "r1")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	ed := FromEd25519(pub)
	unusable := Key{Kty: "oct", Kid: "sym"}

	data, _ := json.Marshal(Set{Keys: []Key{r, ed, unusable}})
	keys, err := ParseSet(data)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, "r1")
	assert.Contains(t, keys, ed.Kid)

	dup, _ := json.Marshal(Set{Keys: []Key{r, r}})
	_, err = ParseSet(dup)
	assert.Error(t, err)

	none, _ := json.Marshal(Set{Keys: []Key{unusable}})
	_, err = ParseSet(none)
	assert.Error(t, err)

	_, err = ParseSet([]byte("not json"))
	assert.Error(t, err)
}

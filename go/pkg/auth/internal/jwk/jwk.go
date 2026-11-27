// Package jwk parses JSON Web Keys (RFC 7517) into Go public keys and
// computes JWK thumbprints (RFC 7638). It is shared by the auth providers
// and is internal so its API can change without notice.
package jwk

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// MinRSABits is the smallest RSA modulus accepted.
const MinRSABits = 2048

// ErrUnusable marks a key that is valid JSON but cannot be used to verify
// signatures (unsupported type or curve, encryption-only, too weak).
var ErrUnusable = errors.New("jwk: unusable key")

// Key is the subset of JWK members DYNAMOS needs.
type Key struct {
	Kty string `json:"kty"`
	Kid string `json:"kid,omitempty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
}

// Set is a JWK Set document.
type Set struct {
	Keys []Key `json:"keys"`
}

// PublicKey converts k into *rsa.PublicKey, *ecdsa.PublicKey or
// ed25519.PublicKey.
func (k Key) PublicKey() (crypto.PublicKey, error) {
	if k.Use != "" && k.Use != "sig" {
		return nil, fmt.Errorf("%w: use %q is not \"sig\"", ErrUnusable, k.Use)
	}
	switch k.Kty {
	case "RSA":
		n, err := decodeInt(k.N)
		if err != nil {
			return nil, fmt.Errorf("jwk: bad RSA modulus: %w", err)
		}
		e, err := decodeInt(k.E)
		if err != nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, fmt.Errorf("jwk: bad RSA exponent")
		}
		if n.BitLen() < MinRSABits {
			return nil, fmt.Errorf("%w: RSA key is %d bits, need %d", ErrUnusable, n.BitLen(), MinRSABits)
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("%w: EC curve %q", ErrUnusable, k.Crv)
		}
		x, err := decodeInt(k.X)
		if err != nil {
			return nil, fmt.Errorf("jwk: bad EC x: %w", err)
		}
		y, err := decodeInt(k.Y)
		if err != nil {
			return nil, fmt.Errorf("jwk: bad EC y: %w", err)
		}
		pub := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		// ECDH() validates that the point is on the curve and not infinity.
		if _, err := pub.ECDH(); err != nil {
			return nil, fmt.Errorf("jwk: EC point is not valid on curve %s", k.Crv)
		}
		return pub, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, fmt.Errorf("%w: OKP curve %q", ErrUnusable, k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("jwk: bad OKP x: %w", err)
		}
		if len(x) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("jwk: Ed25519 key is %d bytes, need %d", len(x), ed25519.PublicKeySize)
		}
		return ed25519.PublicKey(x), nil
	default:
		return nil, fmt.Errorf("%w: kty %q", ErrUnusable, k.Kty)
	}
}

// ParseSet decodes a JWK Set and returns its signing keys by kid. Keys
// that are unusable for signature checks are skipped, as RFC 7517 section
// 5 allows. A key without "kid" is stored under "". It fails when the
// document is malformed, a kid repeats, or no usable key remains.
func ParseSet(data []byte) (map[string]crypto.PublicKey, error) {
	var set Set
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("jwk: bad JWK Set: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.PublicKey()
		if err != nil {
			continue
		}
		if _, dup := keys[k.Kid]; dup {
			return nil, fmt.Errorf("jwk: kid %q appears more than once", k.Kid)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("jwk: JWK Set has no usable signing key")
	}
	return keys, nil
}

// FromEd25519 returns the JWK for an Ed25519 public key, with Kid set to
// its RFC 7638 thumbprint.
func FromEd25519(pub ed25519.PublicKey) Key {
	k := Key{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(pub), Use: "sig", Alg: "EdDSA"}
	k.Kid = ThumbprintEd25519(pub)
	return k
}

// ThumbprintEd25519 returns the RFC 7638 SHA-256 thumbprint of an Ed25519
// public key, base64url encoded. It is a stable key ID derived from the key
// alone, so issuer and verifier agree on it with no extra configuration.
func ThumbprintEd25519(pub ed25519.PublicKey) string {
	// RFC 7638: required members only, in lexicographic order, no spaces.
	canonical := `{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func decodeInt(s string) (*big.Int, error) {
	if s == "" {
		return nil, errors.New("empty value")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

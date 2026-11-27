package jwtauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/internal/jwk"
)

// GenerateKey creates a new Ed25519 signing key.
func GenerateKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	return priv, err
}

// KeyID returns the key ID used in the "kid" header for pub: its RFC 7638
// JWK thumbprint. Issuer and verifier derive it from the key alone.
func KeyID(pub ed25519.PublicKey) string {
	return jwk.ThumbprintEd25519(pub)
}

// MarshalPrivateKeyPEM encodes key as a PKCS #8 "PRIVATE KEY" PEM block.
func MarshalPrivateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// MarshalPublicKeyPEM encodes pub as a PKIX "PUBLIC KEY" PEM block.
func MarshalPublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// ParsePrivateKeyPEM decodes one PKCS #8 Ed25519 private key.
// The same format comes out of: openssl genpkey -algorithm ed25519
func ParsePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("jwtauth: no PKCS #8 \"PRIVATE KEY\" PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwtauth: parsing private key: %w", err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("jwtauth: private key is %T, need Ed25519", key)
	}
	return ed, nil
}

// ParsePublicKeysPEM decodes every PKIX "PUBLIC KEY" block in data. More
// than one key lets verifiers trust the old and the new key during a key
// rotation. Blocks of other types are an error, so a private key pasted
// by mistake is not ignored.
func ParsePublicKeysPEM(data []byte) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("jwtauth: unexpected PEM block %q, need \"PUBLIC KEY\"", block.Type)
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("jwtauth: parsing public key: %w", err)
		}
		ed, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("jwtauth: public key is %T, need Ed25519", key)
		}
		keys = append(keys, ed)
	}
	if len(keys) == 0 {
		return nil, errors.New("jwtauth: no \"PUBLIC KEY\" PEM block found")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("jwtauth: trailing data after PEM blocks")
	}
	return keys, nil
}

// readFiles concatenates the content of every path.
func readFiles(paths []string) ([]byte, error) {
	var out []byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("jwtauth: reading key file: %w", err)
		}
		out = append(out, b...)
		out = append(out, '\n')
	}
	return out, nil
}

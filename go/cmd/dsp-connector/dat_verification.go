package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DAT verification (issue #56): resolves the signing DID from a presented
// token, verifies the token's signature against that DID's published key,
// and uses the verified DID itself as the participant's identity.
//
// Originally scoped to read an email claim from an embedded
// DataStewardCredential (mirroring MVD's own ManufacturerCredential
// pattern) - dropped once it turned out MVD's issuerservice hardcodes its
// supported attestation types ("membership"/"manufacturer" only; a custom
// "data_steward" type is rejected outright, "Unknown attestation type").
// Minting a real third type means writing a new attestation-type provider
// in MVD's own Java/Kotlin source, out of scope here. Using the DID
// directly needs zero MVD changes and is what this file actually does.
//
// DYNAMOS's Relations map (go/pkg/api/http.go) stays keyed by whatever
// string a caller was already using - existing non-DSP callers (unrelated
// to this file entirely) keep using email, unchanged; a DSP-verified caller
// now keys off its DID instead. Both live in the same map; nothing unifies
// them as "the same real party" automatically - an operator seeding both an
// email-keyed and a DID-keyed Relation for the same underlying party is an
// ops concern, not a code one.
//
// Scope, deliberately: this verifies the presented token's own signature
// (proves the caller holds the signing DID's private key) - full DCP
// Presentation Flow compliance (embedded-credential issuer-signature
// chains, formal holder-binding proof) is out of scope for #56, noted as a
// follow-up once a real MVD-issued token's exact wire shape has actually
// been observed.

var (
	// ErrDATInvalid covers every verification failure - deliberately not
	// split into sub-errors the caller could branch on, since the 401
	// response callers give today ("missing-authorization") doesn't
	// distinguish reasons either.
	ErrDATInvalid = errors.New("dat: invalid token")
)

// didResolverFunc resolves a DID to the public key it's allowed to sign
// with. Production uses resolveDIDWeb (real HTTP resolution); tests inject
// a fixture returning an in-memory key, same pattern used elsewhere in this
// package for swappable I/O (see negotiationServiceURL-style package vars).
type didResolverFunc func(did string) (crypto.PublicKey, error)

// datResolver is the active resolver. Overridden in tests.
var datResolver didResolverFunc = resolveDIDWeb

// resolveDIDWeb implements the did:web method's own URL mapping:
// did:web:{host%3Aport}:{path...} -> https://{host}:{port}/{path...}/did.json,
// or https://{host}:{port}/.well-known/did.json with no path segments.
// Percent-encoding in the host segment (%3A for the port colon) is required
// by the did:web spec precisely so the host segment can itself contain a
// colon without being mistaken for another path segment.
func resolveDIDWeb(did string) (crypto.PublicKey, error) {
	docURL, err := didWebDocumentURL(did)
	if err != nil {
		return nil, err
	}

	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(docURL)
	if err != nil {
		return nil, fmt.Errorf("%w: fetching DID document: %v", ErrDATInvalid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: DID document fetch returned %d", ErrDATInvalid, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading DID document: %v", ErrDATInvalid, err)
	}

	var doc struct {
		VerificationMethod []struct {
			ID           string `json:"id"`
			PublicKeyJwk *jwk   `json:"publicKeyJwk"`
		} `json:"verificationMethod"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: parsing DID document: %v", ErrDATInvalid, err)
	}
	if len(doc.VerificationMethod) == 0 || doc.VerificationMethod[0].PublicKeyJwk == nil {
		return nil, fmt.Errorf("%w: DID document has no usable verificationMethod", ErrDATInvalid)
	}

	return doc.VerificationMethod[0].PublicKeyJwk.publicKey()
}

// didWebDocumentURL is the pure did:web -> URL mapping, split out from
// resolveDIDWeb so it's unit-testable without live HTTP.
//
// Scheme is didWebScheme, not a hardcoded "https" - the DID spec mandates
// https, and that's the default (config_prod.go). Local/TCK builds
// (config_local.go) set it to "http": there's no real TLS anywhere in this
// demo/harness setup, and MVD's own local deployment makes the identical
// call for the identical reason (edc.iam.did.web.use.https: "false" in its
// issuerservice ConfigMap) - precedent from the same upstream project this
// whole identity layer is borrowed from, not a one-off shortcut.
func didWebDocumentURL(did string) (string, error) {
	const prefix = "did:web:"
	if !strings.HasPrefix(did, prefix) {
		return "", fmt.Errorf("%w: unsupported DID method %q", ErrDATInvalid, did)
	}
	segments := strings.Split(strings.TrimPrefix(did, prefix), ":")
	if len(segments) == 0 || segments[0] == "" {
		return "", fmt.Errorf("%w: empty did:web host segment", ErrDATInvalid)
	}
	host, err := url.PathUnescape(segments[0])
	if err != nil {
		return "", fmt.Errorf("%w: bad did:web host segment: %v", ErrDATInvalid, err)
	}

	if len(segments) == 1 {
		return fmt.Sprintf("%s://%s/.well-known/did.json", didWebScheme, host), nil
	}

	pathSegments := make([]string, len(segments)-1)
	for i, seg := range segments[1:] {
		decoded, err := url.PathUnescape(seg)
		if err != nil {
			return "", fmt.Errorf("%w: bad did:web path segment: %v", ErrDATInvalid, err)
		}
		pathSegments[i] = decoded
	}
	return fmt.Sprintf("%s://%s/%s/did.json", didWebScheme, host, strings.Join(pathSegments, "/")), nil
}

// jwk is the minimal subset of RFC 7517 needed for the two key types MVD's
// seeded participants actually use (EC/P-256 and OKP/Ed25519) - not a
// general-purpose JWK implementation, deliberately, since that's already a
// large surface a dedicated library would normally own; hand-rolled here to
// avoid pulling in a second dependency (see go.mod diff for #56 - the only
// candidate JWK library also usable for JWT, jwx/v2, is deprecated upstream
// and forces a repo-wide Go toolchain bump this issue has no reason to make).
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k *jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("%w: unsupported EC curve %q", ErrDATInvalid, k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("%w: bad EC x coordinate: %v", ErrDATInvalid, err)
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("%w: bad EC y coordinate: %v", ErrDATInvalid, err)
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, fmt.Errorf("%w: unsupported OKP curve %q", ErrDATInvalid, k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("%w: bad OKP x coordinate: %v", ErrDATInvalid, err)
		}
		if len(x) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: bad Ed25519 public key length", ErrDATInvalid)
		}
		return ed25519.PublicKey(x), nil
	default:
		return nil, fmt.Errorf("%w: unsupported JWK kty %q", ErrDATInvalid, k.Kty)
	}
}

// verifyDAT verifies a presented DAT's signature against its own claimed
// signing DID and, on success, returns that DID as the participant's
// identity - see the file doc comment for why the identity is the DID
// itself rather than a claim read out of an embedded credential.
func verifyDAT(token string) (string, error) {
	unverified, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDATInvalid, err)
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("%w: unexpected claims shape", ErrDATInvalid)
	}
	holderDID, ok := claims["iss"].(string)
	if !ok || holderDID == "" {
		return "", fmt.Errorf("%w: missing iss claim", ErrDATInvalid)
	}

	pubKey, err := datResolver(holderDID)
	if err != nil {
		return "", err
	}

	verified, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		return pubKey, nil
	}, jwt.WithValidMethods([]string{"ES256", "EdDSA"}))
	if err != nil || !verified.Valid {
		return "", fmt.Errorf("%w: signature verification failed: %v", ErrDATInvalid, err)
	}

	return holderDID, nil
}

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
// and extracts the participant's email from an embedded DataStewardCredential
// claim (DYNAMOS's own credential type - minted during dataspace seeding
// specifically to carry the email DYNAMOS's Relations map is keyed by;
// MVD's stock MembershipCredential/ManufacturerCredential don't carry one).
//
// Scope, deliberately: this verifies the presented token's own signature
// (proves the caller holds the signing DID's private key) but does not
// re-verify each embedded credential's own issuer signature - full DCP
// Presentation Flow compliance (holder-binding proof, per-credential issuer
// chains) is out of scope for #56, noted as a follow-up once a real
// MVD-issued token's exact wire shape has actually been observed.

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
		return fmt.Sprintf("https://%s/.well-known/did.json", host), nil
	}

	pathSegments := make([]string, len(segments)-1)
	for i, seg := range segments[1:] {
		decoded, err := url.PathUnescape(seg)
		if err != nil {
			return "", fmt.Errorf("%w: bad did:web path segment: %v", ErrDATInvalid, err)
		}
		pathSegments[i] = decoded
	}
	return fmt.Sprintf("https://%s/%s/did.json", host, strings.Join(pathSegments, "/")), nil
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

// dataStewardCredentialType is DYNAMOS's own DCP credential type, minted
// during dataspace seeding (see wiki/decisions/ADR-009-simulated-dataspace-via-mvd.md
// and the mvd-demo-dataspace-setup runbook), carrying the email claim
// DYNAMOS's Relations map is keyed by - not part of MVD's own stock
// credential set.
const dataStewardCredentialType = "DataStewardCredential"

// verifyDAT verifies a presented DAT and returns the participant email from
// its embedded DataStewardCredential. The outer token's own signature is
// verified cryptographically against its issuer DID; embedded credentials
// are read for claims only, not signature-checked (see the file doc comment
// for why).
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
	verifiedClaims := verified.Claims.(jwt.MapClaims)

	email, err := emailFromEmbeddedCredentials(verifiedClaims)
	if err != nil {
		return "", err
	}
	return email, nil
}

// emailFromEmbeddedCredentials walks the token's embedded credentials
// looking for a DataStewardCredential, tolerating two possible wire shapes
// (a DCP-style "vp.verifiableCredential[]", or a flatter top-level
// "credentials"/"vc" array) - the exact shape MVD's own connector will send
// on a live request hasn't been observed yet, see the file doc comment.
func emailFromEmbeddedCredentials(claims jwt.MapClaims) (string, error) {
	var rawCredentials []interface{}

	if vp, ok := claims["vp"].(map[string]interface{}); ok {
		if vcs, ok := vp["verifiableCredential"].([]interface{}); ok {
			rawCredentials = vcs
		}
	}
	if rawCredentials == nil {
		if vcs, ok := claims["credentials"].([]interface{}); ok {
			rawCredentials = vcs
		}
	}
	if rawCredentials == nil {
		if vcs, ok := claims["vc"].([]interface{}); ok {
			rawCredentials = vcs
		}
	}

	for _, raw := range rawCredentials {
		credJWT, ok := raw.(string)
		if !ok {
			continue
		}
		email, ok := dataStewardEmailFromCredentialJWT(credJWT)
		if ok {
			return email, nil
		}
	}

	return "", fmt.Errorf("%w: no DataStewardCredential found", ErrDATInvalid)
}

// dataStewardEmailFromCredentialJWT parses (without signature verification,
// see file doc comment) one embedded credential JWT and returns its email
// claim if it's a DataStewardCredential.
func dataStewardEmailFromCredentialJWT(credJWT string) (string, bool) {
	token, _, err := jwt.NewParser().ParseUnverified(credJWT, jwt.MapClaims{})
	if err != nil {
		return "", false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", false
	}

	vc, ok := claims["vc"].(map[string]interface{})
	if !ok {
		vc = claims
	}

	if !credentialHasType(vc, dataStewardCredentialType) {
		return "", false
	}

	subject, ok := vc["credentialSubject"].(map[string]interface{})
	if !ok {
		return "", false
	}
	email, ok := subject["email"].(string)
	if !ok || email == "" {
		return "", false
	}
	return email, true
}

func credentialHasType(vc map[string]interface{}, want string) bool {
	switch t := vc["type"].(type) {
	case string:
		return t == want
	case []interface{}:
		for _, v := range t {
			if s, ok := v.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

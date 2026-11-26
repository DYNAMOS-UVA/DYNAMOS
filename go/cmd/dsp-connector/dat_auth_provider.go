package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
)

// datProviderName is the registry name of the DSP DAT provider.
const datProviderName = "dat"

// The DAT provider plugs the existing verifyDAT into the shared
// pkg/auth contracts, so DSP participant tokens fit the same
// Verifier/Principal model as every other auth method. verifyDAT itself is
// unchanged and the DSP handlers still call it directly through
// participantFromRequest; moving them onto auth.Middleware, and moving
// this provider into pkg/auth, is a later step.
//
// Known limit: verifyDAT wraps every failure, including a DID document
// fetch failure, in ErrDATInvalid, so this adapter cannot tell an
// unreachable DID host (auth.ErrUnavailable) from a bad token. Both
// answer auth.ErrInvalidCredentials.
func init() {
	auth.Register(datProviderName, func(auth.Settings) (auth.Verifier, error) {
		return datVerifier{}, nil
	})
}

type datVerifier struct{}

var _ auth.Verifier = datVerifier{}

// Verify accepts a DAT whose "iss" is a DID and whose signature matches
// that DID's published key. Tokens with a non-DID issuer answer
// auth.ErrUnsupportedToken, so auth.Any can route them to other providers
// without a DID resolution round trip.
func (datVerifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	peek := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, peek); err != nil {
		return auth.Principal{}, fmt.Errorf("%w: not a JWT", auth.ErrUnsupportedToken)
	}
	if iss, _ := peek["iss"].(string); !strings.HasPrefix(iss, "did:") {
		return auth.Principal{}, fmt.Errorf("%w: issuer is not a DID", auth.ErrUnsupportedToken)
	}

	did, err := verifyDAT(token)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %v", auth.ErrInvalidCredentials, err)
	}
	return auth.Principal{
		Subject:  did,
		Issuer:   did,
		Kind:     auth.KindParticipant,
		Provider: datProviderName,
	}, nil
}

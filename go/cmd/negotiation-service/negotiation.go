package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// State is one of the 7 DSP contract negotiation states
// (docs/negotiation/dsp-negotiation-state-machine.md).
type State string

const (
	StateRequested  State = "REQUESTED"
	StateOffered    State = "OFFERED"
	StateAccepted   State = "ACCEPTED"
	StateAgreed     State = "AGREED"
	StateVerified   State = "VERIFIED"
	StateFinalized  State = "FINALIZED"
	StateTerminated State = "TERMINATED"
)

// ErrNegotiationNotFound / ErrInvalidTransition: sentinels so the internal
// API can tell a business error (404 / 409) apart from an etcd I/O failure (500).
var (
	ErrNegotiationNotFound = errors.New("no negotiation found for id")
	ErrInvalidTransition   = errors.New("state does not allow this transition")
)

// Negotiation is negotiation-service's own etcd schema, own key namespace
// (/dsp/negotiations/{party}/{id}) - no shared schema with non-DSP keys.
// Offer/Agreement are stored opaque (raw ODRL JSON-LD as carried by the DSP
// message) - negotiation-service only owns the state machine, it doesn't
// interpret ODRL semantics (that's T2.4's job, against the FINALIZED value).
type Negotiation struct {
	ProviderPid string          `json:"providerPid"`
	ConsumerPid string          `json:"consumerPid"`
	Party       string          `json:"party"`
	State       State           `json:"state"`
	Offer       json.RawMessage `json:"offer,omitempty"`
	Agreement   json.RawMessage `json:"agreement,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// newNegotiation builds a fresh negotiation in REQUESTED - the initiating
// Contract Request Message (no providerPid yet).
func newNegotiation(party, consumerPid string, offer json.RawMessage) *Negotiation {
	now := time.Now().UTC()
	return &Negotiation{
		ProviderPid: fmt.Sprintf("urn:dynamos:negotiation:%s:%s", party, uuid.New().String()),
		ConsumerPid: consumerPid,
		Party:       party,
		State:       StateRequested,
		Offer:       offer,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// transition moves the negotiation to `to`, only if its current state is one
// of `from` - every internal-API write handler calls this with the source
// states the corresponding DSP message allows (see the doc's state table).
// TERMINATED is always a dead end: never a valid `from`.
func (n *Negotiation) transition(to State, from ...State) error {
	if n.State == StateTerminated {
		return fmt.Errorf("%w: negotiation %q is TERMINATED", ErrInvalidTransition, n.ProviderPid)
	}

	if !slices.Contains(from, n.State) {
		return fmt.Errorf("%w: %q -> %q (currently %q)", ErrInvalidTransition, n.State, to, n.State)
	}

	n.State = to
	n.UpdatedAt = time.Now().UTC()
	return nil
}

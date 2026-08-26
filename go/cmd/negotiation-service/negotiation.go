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

// Kind marks which side of the negotiation DYNAMOS itself plays - the two
// roles are not exclusive, a single negotiation-service instance can hold
// both a Provider-role and a Consumer-role negotiation at once, so every
// Negotiation must say which one it is (#80).
type Kind string

const (
	KindProvider Kind = "Provider"
	KindConsumer Kind = "Consumer"
)

// Negotiation is negotiation-service's own etcd schema, own key namespace
// (/dsp/negotiations/{party}/{id}) - no shared schema with non-DSP keys.
// Offer/Agreement are stored opaque (raw ODRL JSON-LD as carried by the DSP
// message) - negotiation-service only owns the state machine, it doesn't
// interpret ODRL semantics (that's T2.4's job, against the FINALIZED value).
type Negotiation struct {
	// Kind defaults to KindProvider (the zero value is "") only through
	// newNegotiation explicitly setting it - never leave this unset on a
	// hand-built Negotiation, Store keys on it (see OwnPid/negotiationKey).
	Kind        Kind   `json:"kind"`
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
	Party       string `json:"party"`
	// Participant is the other side's identity, captured at creation time -
	// for a Provider-role negotiation, the external Consumer's identity
	// (dsp-connector's Authorization-header value, see participantFromRequest
	// in go/cmd/dsp-connector/catalog_handler.go); for a Consumer-role
	// negotiation, DYNAMOS's own identity, sent as the Authorization header
	// on the outbound Contract Request Message. negotiation-service never
	// interprets it - it's stored opaque purely so dsp-connector can check,
	// on every later provider-endpoint call, that the caller is the same
	// participant who opened this negotiation.
	Participant string `json:"participant"`
	// RemoteParticipant is only set on a Consumer-role negotiation: the
	// identity of the remote Provider DYNAMOS expects to hear back from,
	// declared once at creation (#81) - the caller of create-as-consumer
	// already has to know who they're negotiating with to pick a
	// providerEndpoint, so this costs nothing extra to require. dsp-connector
	// compares every inbound Consumer callback's DAT-verified caller against
	// this value (mirrors T2.3's ownership check, which uses Participant the
	// same way for a Provider-role negotiation) - immutable, never
	// "locked in" on first contact, so there's no window where an
	// unauthenticated first caller could claim a negotiation.
	RemoteParticipant string `json:"remoteParticipant,omitempty"`
	// ProviderEndpoint is only set on a Consumer-role negotiation: the
	// remote Provider's DSP base URL, captured once at creation (#80) and
	// reused by #82's autonomous accept-all logic to send the outbound
	// ACCEPTED event and Agreement Verification Message later in the same
	// negotiation - both go to ProviderEndpoint+"/negotiations/"+ProviderPid+"/<path>",
	// same resolution rule CallbackAddress already documents for the
	// opposite direction.
	ProviderEndpoint string          `json:"providerEndpoint,omitempty"`
	State            State           `json:"state"`
	Offer            json.RawMessage `json:"offer,omitempty"`
	Agreement        json.RawMessage `json:"agreement,omitempty"`
	// CallbackAddress is the consumer's callback base URL from the
	// initiating Contract Request Message (DSP HTTPS binding's Consumer
	// Path Bindings - every provider-initiated message this negotiation
	// sends later gets POSTed to CallbackAddress+"/negotiations/"+ConsumerPid+"/<path>",
	// per contract.negotiation.binding.https.md). Captured once at creation,
	// same as Participant - the DSP spec doesn't let it change mid-negotiation.
	CallbackAddress string    `json:"callbackAddress,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// newNegotiation builds a fresh Provider-role negotiation in REQUESTED - the
// initiating Contract Request Message (no providerPid yet, from the caller's
// side; DYNAMOS is Provider so it owns and generates the ProviderPid here).
func newNegotiation(party, consumerPid, participant, callbackAddress string, offer json.RawMessage) *Negotiation {
	now := time.Now().UTC()
	return &Negotiation{
		Kind:            KindProvider,
		ProviderPid:     fmt.Sprintf("urn:dynamos:negotiation:%s:%s", party, uuid.New().String()),
		ConsumerPid:     consumerPid,
		Party:           party,
		Participant:     participant,
		State:           StateRequested,
		Offer:           offer,
		CallbackAddress: callbackAddress,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// newConsumerNegotiation builds a fresh Consumer-role negotiation in
// REQUESTED, the moment DYNAMOS itself sends the initiating Contract Request
// Message. DYNAMOS is Consumer here, so it owns and generates the
// ConsumerPid; ProviderPid is unknown until the remote Provider's 201
// response comes back (see negotiationsConsumerCollectionHandler).
// remoteParticipant is the Provider's identity - see Negotiation.RemoteParticipant.
// providerEndpoint is the Provider's DSP base URL - see Negotiation.ProviderEndpoint.
func newConsumerNegotiation(party, participant, remoteParticipant, providerEndpoint, callbackAddress string, offer json.RawMessage) *Negotiation {
	now := time.Now().UTC()
	return &Negotiation{
		Kind:              KindConsumer,
		ConsumerPid:       fmt.Sprintf("urn:dynamos:negotiation:consumer:%s:%s", party, uuid.New().String()),
		Party:             party,
		Participant:       participant,
		RemoteParticipant: remoteParticipant,
		ProviderEndpoint:  providerEndpoint,
		State:             StateRequested,
		Offer:             offer,
		CallbackAddress:   callbackAddress,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// OwnPid returns the Pid DYNAMOS itself owns for this negotiation - the one
// Store keys on (see negotiationKey). A Provider-role negotiation owns
// ProviderPid (DYNAMOS generated it); a Consumer-role negotiation owns
// ConsumerPid, for the same reason.
func (n *Negotiation) OwnPid() string {
	if n.Kind == KindConsumer {
		return n.ConsumerPid
	}
	return n.ProviderPid
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

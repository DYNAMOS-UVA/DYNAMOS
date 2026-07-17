package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNegotiation(t *testing.T) {
	n := newNegotiation("VU", "urn:example:consumer:1", "consumer@example.com", []byte(`{"@id":"offer-1"}`))

	assert.Equal(t, "VU", n.Party)
	assert.Equal(t, "urn:example:consumer:1", n.ConsumerPid)
	assert.Equal(t, "consumer@example.com", n.Participant)
	assert.Equal(t, StateRequested, n.State)
	assert.Contains(t, n.ProviderPid, "urn:dynamos:negotiation:VU:")
	assert.False(t, n.CreatedAt.IsZero())
	assert.Equal(t, n.CreatedAt, n.UpdatedAt)
}

func TestTransition_ValidPath(t *testing.T) {
	n := newNegotiation("VU", "urn:example:consumer:1", "consumer@example.com", []byte(`{}`))

	require.NoError(t, n.transition(StateOffered, StateRequested, StateOffered))
	assert.Equal(t, StateOffered, n.State)

	require.NoError(t, n.transition(StateAccepted, StateOffered))
	assert.Equal(t, StateAccepted, n.State)

	require.NoError(t, n.transition(StateAgreed, StateAccepted))
	assert.Equal(t, StateAgreed, n.State)

	require.NoError(t, n.transition(StateVerified, StateAgreed))
	assert.Equal(t, StateVerified, n.State)

	require.NoError(t, n.transition(StateFinalized, StateVerified))
	assert.Equal(t, StateFinalized, n.State)
}

func TestTransition_RejectsWrongSourceState(t *testing.T) {
	n := newNegotiation("VU", "urn:example:consumer:1", "consumer@example.com", []byte(`{}`))

	err := n.transition(StateVerified, StateAgreed)
	assert.ErrorIs(t, err, ErrInvalidTransition)
	assert.Equal(t, StateRequested, n.State, "rejected transition must not mutate state")
}

func TestTransition_TerminatedIsDeadEnd(t *testing.T) {
	n := newNegotiation("VU", "urn:example:consumer:1", "consumer@example.com", []byte(`{}`))
	require.NoError(t, n.transition(StateTerminated, StateRequested))

	err := n.transition(StateOffered, StateRequested, StateOffered, StateTerminated)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestNegotiation_Clone(t *testing.T) {
	n := newNegotiation("VU", "urn:example:consumer:1", "consumer@example.com", []byte(`{"@id":"offer-1"}`))
	c := n.clone()

	assert.Equal(t, n.ProviderPid, c.ProviderPid)
	assert.JSONEq(t, string(n.Offer), string(c.Offer))

	// Mutating the clone's Offer (or the clone's state) must never reach
	// back into the original - the whole point of clone() is that Store's
	// cached copy and every Get caller's copy are fully independent.
	c.Offer[2] = 'X'
	c.State = StateTerminated
	assert.NotEqual(t, string(n.Offer), string(c.Offer))
	assert.Equal(t, StateRequested, n.State)
}

func TestTransition_OfferedRequestedLoop(t *testing.T) {
	// Both REQUESTED and OFFERED can be reached repeatedly before AGREED -
	// counter-request/counter-offer, per the state machine doc.
	n := newNegotiation("VU", "urn:example:consumer:1", "consumer@example.com", []byte(`{}`))

	require.NoError(t, n.transition(StateOffered, StateRequested, StateOffered))
	require.NoError(t, n.transition(StateRequested, StateOffered))
	require.NoError(t, n.transition(StateOffered, StateRequested, StateOffered))
	assert.Equal(t, StateOffered, n.State)
}

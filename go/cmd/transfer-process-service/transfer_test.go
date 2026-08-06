package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTransferProcess(t *testing.T) {
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)

	assert.Equal(t, "VU", tp.Party)
	assert.Equal(t, "urn:example:consumer:1", tp.ConsumerPid)
	assert.Equal(t, "consumer@example.com", tp.Participant)
	assert.Equal(t, "urn:example:agreement:1", tp.AgreementId)
	assert.Equal(t, "example:HTTP_PULL", tp.Format)
	assert.Equal(t, StateRequested, tp.State)
	assert.Contains(t, tp.ProviderPid, "urn:dynamos:transfer:VU:")
	assert.False(t, tp.CreatedAt.IsZero())
	assert.Equal(t, tp.CreatedAt, tp.UpdatedAt)
}

func TestTransition_ValidPath(t *testing.T) {
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)

	require.NoError(t, tp.transition(StateStarted, StateRequested, StateSuspended))
	assert.Equal(t, StateStarted, tp.State)

	require.NoError(t, tp.transition(StateCompleted, StateStarted))
	assert.Equal(t, StateCompleted, tp.State)
}

func TestTransition_SuspendResumeLoop(t *testing.T) {
	// STARTED and SUSPENDED can loop before COMPLETED. This pause/resume
	// path does not exist in negotiation's state machine.
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)

	require.NoError(t, tp.transition(StateStarted, StateRequested, StateSuspended))
	require.NoError(t, tp.transition(StateSuspended, StateStarted))
	require.NoError(t, tp.transition(StateStarted, StateRequested, StateSuspended))
	assert.Equal(t, StateStarted, tp.State)
}

func TestTransition_RejectsWrongSourceState(t *testing.T) {
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)

	err := tp.transition(StateCompleted, StateStarted)
	assert.ErrorIs(t, err, ErrInvalidTransition)
	assert.Equal(t, StateRequested, tp.State, "rejected transition must not mutate state")
}

func TestTransition_CompletedIsDeadEnd(t *testing.T) {
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)
	require.NoError(t, tp.transition(StateStarted, StateRequested, StateSuspended))
	require.NoError(t, tp.transition(StateCompleted, StateStarted))

	err := tp.transition(StateTerminated, StateRequested, StateStarted, StateSuspended, StateCompleted)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestTransition_TerminatedIsDeadEnd(t *testing.T) {
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", nil)
	require.NoError(t, tp.transition(StateTerminated, StateRequested))

	err := tp.transition(StateStarted, StateRequested, StateSuspended, StateTerminated)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestNewConsumerTransferProcess(t *testing.T) {
	tp := newConsumerTransferProcess("VU", "urn:dynamos:party:VU", "urn:dynamos:party:UVA", "https://uva.example.com/api/v1", "urn:example:agreement:1", "dynamos:computeToData", "https://vu.example.com/api/v1/callback", nil)

	assert.Equal(t, KindConsumer, tp.Kind)
	assert.Equal(t, "VU", tp.Party)
	assert.Equal(t, "urn:dynamos:party:VU", tp.Participant)
	assert.Equal(t, "urn:dynamos:party:UVA", tp.RemoteParticipant)
	assert.Equal(t, "https://uva.example.com/api/v1", tp.ProviderEndpoint)
	assert.Equal(t, "urn:example:agreement:1", tp.AgreementId)
	assert.Equal(t, StateRequested, tp.State)
	assert.Contains(t, tp.ConsumerPid, "urn:dynamos:transfer:consumer:VU:")
	assert.Empty(t, tp.ProviderPid, "ProviderPid is unknown until the remote Provider's 201 response")
}

func TestTransferProcess_Clone(t *testing.T) {
	tp := newTransferProcess("VU", "urn:example:consumer:1", "consumer@example.com", "urn:example:agreement:1", "example:HTTP_PULL", "https://consumer.example.com/callback", []byte(`{"endpoint":"http://example.com"}`))
	c := tp.clone()

	assert.Equal(t, tp.ProviderPid, c.ProviderPid)
	assert.JSONEq(t, string(tp.DataAddress), string(c.DataAddress))

	// A change to the clone's DataAddress, or to the clone's state, must
	// never reach back into the original. clone() exists to keep Store's
	// cached copy and every Get caller's copy fully independent.
	c.DataAddress[2] = 'X'
	c.State = StateTerminated
	assert.NotEqual(t, string(tp.DataAddress), string(c.DataAddress))
	assert.Equal(t, StateRequested, tp.State)
}

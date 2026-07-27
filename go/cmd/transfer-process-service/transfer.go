package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// State is one of the 5 DSP transfer process states
// (docs/transfer/dsp-transfer-state-machine.md).
type State string

const (
	StateRequested  State = "REQUESTED"
	StateStarted    State = "STARTED"
	StateCompleted  State = "COMPLETED"
	StateSuspended  State = "SUSPENDED"
	StateTerminated State = "TERMINATED"
)

// ErrTransferNotFound and ErrInvalidTransition are sentinel errors. The
// internal API uses them to tell a business error (404 or 409) from an
// etcd I/O failure (500).
var (
	ErrTransferNotFound  = errors.New("no transfer found for id")
	ErrInvalidTransition = errors.New("state does not allow this transition")
)

// TransferProcess is transfer-process-service's own etcd schema. It uses
// its own key namespace (/dsp/transfers/{id}), separate from non-DSP keys.
// DataAddress stays opaque: raw transport-specific JSON, as carried by the
// DSP message. transfer-process-service owns the state machine only. It
// does not read the transport details. The data plane handles that job.
// The DSP Transfer Process Protocol does not cover the data plane either.
type TransferProcess struct {
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
	Party       string `json:"party"`
	// Participant holds the requesting participant's identity. This is
	// dsp-connector's Authorization-header value, captured at creation
	// time. transfer-process-service never reads this value. It stays
	// opaque. dsp-connector uses it on every later provider-endpoint call,
	// to check the caller is the same participant who opened this
	// transfer. This follows the same convention as negotiation-service's
	// Negotiation.Participant field.
	Participant string `json:"participant"`
	State       State  `json:"state"`
	// AgreementId names the Agreement this transfer runs under. It stays
	// opaque: transfer-process-service does not check it against a
	// FINALIZED negotiation. Which service owns that check is still an
	// open question - see the "Still open" section in
	// docs/transfer/dsp-transfer-state-machine.md.
	AgreementId string `json:"agreementId"`
	// Format is the Distribution format from the Transfer Request Message.
	// It comes from the Provider's Catalog.
	Format      string          `json:"format,omitempty"`
	DataAddress json.RawMessage `json:"dataAddress,omitempty"`
	// CallbackAddress is the consumer's callback base URL. It comes from
	// the initiating Transfer Request Message. Per the DSP HTTPS binding's
	// Consumer Callback Path Bindings, every provider-initiated message
	// goes to CallbackAddress+"/transfers/"+ConsumerPid+"/<path>" (see
	// transfer.process.binding.https.md). transfer-process-service
	// captures this value once, at creation time, same as Participant.
	// The DSP spec does not allow this value to change mid-transfer.
	CallbackAddress string    `json:"callbackAddress,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// newTransferProcess builds a new transfer in state REQUESTED. This matches
// the initiating Transfer Request Message, which has no providerPid yet.
func newTransferProcess(party, consumerPid, participant, agreementId, format, callbackAddress string, dataAddress json.RawMessage) *TransferProcess {
	now := time.Now().UTC()
	return &TransferProcess{
		ProviderPid:     fmt.Sprintf("urn:dynamos:transfer:%s:%s", party, uuid.New().String()),
		ConsumerPid:     consumerPid,
		Party:           party,
		Participant:     participant,
		State:           StateRequested,
		AgreementId:     agreementId,
		Format:          format,
		DataAddress:     dataAddress,
		CallbackAddress: callbackAddress,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// transition moves the transfer to state `to`, only if the current state is
// in `from`. Every internal-API write handler calls transition with the
// source states its DSP message allows (see the message table in the doc).
// COMPLETED and TERMINATED are both dead ends. Negotiation-service has one
// terminal state. This state machine has two. Neither COMPLETED nor
// TERMINATED is ever a valid `from` state.
func (t *TransferProcess) transition(to State, from ...State) error {
	if t.State == StateCompleted || t.State == StateTerminated {
		return fmt.Errorf("%w: transfer %q is %s", ErrInvalidTransition, t.ProviderPid, t.State)
	}

	if !slices.Contains(from, t.State) {
		return fmt.Errorf("%w: %q -> %q (currently %q)", ErrInvalidTransition, t.State, to, t.State)
	}

	t.State = to
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// clone makes a deep copy of t, including the DataAddress byte slice. The
// cache and every Get caller must never share one mutable *TransferProcess.
// If they did, one request's state change could corrupt another request's
// read.
func (t TransferProcess) clone() *TransferProcess {
	c := t
	if t.DataAddress != nil {
		c.DataAddress = append(json.RawMessage(nil), t.DataAddress...)
	}
	return &c
}

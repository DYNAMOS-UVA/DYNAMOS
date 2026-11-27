package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/dsp"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
)

// ErrAgreementNotFound means that no usable agreement has this id for this
// participant at this steward. A negotiation of another participant or
// another steward gives the same error, so a caller cannot find out which
// agreement ids exist.
var ErrAgreementNotFound = errors.New("no agreement for this participant")

// ErrAgreementNotFinalized means that the negotiation exists and belongs to
// the participant, but it is not FINALIZED.
var ErrAgreementNotFinalized = errors.New("negotiation is not FINALIZED")

// readEtcdValue returns the value at key, or nil when the key does not
// exist. Tests replace it.
var readEtcdValue = func(key string) ([]byte, error) {
	return etcd.GetByteValueFromEtcd(etcdClient, key)
}

// checkAgreement makes sure that a Provider-role transfer for participant
// can run under agreementID. dsp-connector does the same check, but this
// service starts the job and mints the api-gateway token for participant,
// so it does not trust its caller for this.
//
// agreementID is the providerPid of the negotiation (DYNAMOS and the DSP
// TCK send this) or the "@id" of its Agreement (an external EDC consumer
// sends this). The negotiation must:
//   - be a Provider-role negotiation of this steward (party),
//   - belong to participant,
//   - be FINALIZED.
//
// The negotiations are in the same etcd cluster as the transfers. This
// service only reads them; negotiation-service owns them.
func checkAgreement(participant, agreementID string) error {
	n, err := findProviderNegotiation(agreementID)
	if err != nil {
		return err
	}
	if n.Kind != "" && n.Kind != dsp.NegotiationKindProvider {
		return fmt.Errorf("%w: %q is not a Provider-role negotiation", ErrAgreementNotFound, agreementID)
	}
	if n.Party != party {
		return fmt.Errorf("%w: %q belongs to another steward", ErrAgreementNotFound, agreementID)
	}
	if n.Participant != participant {
		return fmt.Errorf("%w: %q belongs to another participant", ErrAgreementNotFound, agreementID)
	}
	if n.State != dsp.NegotiationFinalized {
		return fmt.Errorf("%w: %q is %q", ErrAgreementNotFinalized, agreementID, n.State)
	}
	return nil
}

// findProviderNegotiation reads the negotiation by providerPid first, then
// through the Agreement "@id" index. Same order as dsp-connector's
// validateAgreementId.
func findProviderNegotiation(agreementID string) (*dsp.NegotiationRecord, error) {
	n, err := readNegotiation(dsp.ProviderNegotiationKey(agreementID))
	if err != nil || n != nil {
		return n, err
	}

	providerPid, err := readEtcdValue(dsp.AgreementIndexKey(agreementID))
	if err != nil {
		return nil, fmt.Errorf("reading agreement index: %w", err)
	}
	if len(providerPid) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrAgreementNotFound, agreementID)
	}
	n, err = readNegotiation(dsp.ProviderNegotiationKey(string(providerPid)))
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fmt.Errorf("%w: %q", ErrAgreementNotFound, agreementID)
	}
	return n, nil
}

// readNegotiation returns nil, nil when key does not exist.
func readNegotiation(key string) (*dsp.NegotiationRecord, error) {
	raw, err := readEtcdValue(key)
	if err != nil {
		return nil, fmt.Errorf("reading negotiation: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var n dsp.NegotiationRecord
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("decoding negotiation %s: %w", key, err)
	}
	return &n, nil
}

// consumerNegotiation returns the Consumer-role negotiation that a
// Consumer-role transfer runs under. negotiationID is its consumerPid.
// The negotiation must be of this steward and FINALIZED, and it must know
// the remote Provider. The caller takes the Provider's endpoint and
// identity and the agreementId from the returned record, never from its
// own request body.
func consumerNegotiation(negotiationID string) (*dsp.NegotiationRecord, error) {
	n, err := readNegotiation(dsp.ConsumerNegotiationKey(negotiationID))
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fmt.Errorf("%w: no Consumer-role negotiation %q", ErrAgreementNotFound, negotiationID)
	}
	if n.Kind != dsp.NegotiationKindConsumer {
		return nil, fmt.Errorf("%w: %q is not a Consumer-role negotiation", ErrAgreementNotFound, negotiationID)
	}
	if n.Party != party {
		return nil, fmt.Errorf("%w: %q belongs to another steward", ErrAgreementNotFound, negotiationID)
	}
	if n.State != dsp.NegotiationFinalized {
		return nil, fmt.Errorf("%w: %q is %q", ErrAgreementNotFinalized, negotiationID, n.State)
	}
	if n.ProviderPid == "" || n.ProviderEndpoint == "" || n.RemoteParticipant == "" {
		return nil, fmt.Errorf("%w: %q does not name its remote Provider", ErrAgreementNotFound, negotiationID)
	}
	return n, nil
}

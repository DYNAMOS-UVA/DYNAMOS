// Package dsp holds the names that more than one DSP service uses for the
// same stored data. negotiation-service writes the negotiations;
// transfer-process-service reads them to check an agreement. Both sides
// use these helpers, so the keys and states cannot drift apart.
package dsp

import "fmt"

// NegotiationFinalized is the state of a negotiation that has an agreement
// a transfer can run under.
const NegotiationFinalized = "FINALIZED"

// Negotiation kinds (the side DYNAMOS plays). A record from before the kind
// field existed has an empty kind; it is a Provider-role record.
const (
	NegotiationKindProvider = "Provider"
	NegotiationKindConsumer = "Consumer"
)

// ProviderNegotiationKey is the etcd key of a Provider-role negotiation.
// id is its providerPid.
func ProviderNegotiationKey(id string) string {
	return fmt.Sprintf("/dsp/negotiations/%s", id)
}

// ConsumerNegotiationKey is the etcd key of a Consumer-role negotiation.
// id is its consumerPid.
func ConsumerNegotiationKey(id string) string {
	return fmt.Sprintf("/dsp/negotiations/consumer/%s", id)
}

// AgreementIndexKey is the etcd key that maps the "@id" of an Agreement to
// the providerPid of the negotiation that made it. The value is the
// providerPid.
func AgreementIndexKey(agreementID string) string {
	return fmt.Sprintf("/dsp/negotiations/by-agreement-id/%s", agreementID)
}

// NegotiationRecord is the part of a stored negotiation that a transfer
// check reads. The JSON names are those of negotiation-service's
// Negotiation.
type NegotiationRecord struct {
	Kind        string `json:"kind"`
	ProviderPid string `json:"providerPid"`
	ConsumerPid string `json:"consumerPid"`
	Party       string `json:"party"`
	Participant string `json:"participant"`
	State       string `json:"state"`
	// RemoteParticipant and ProviderEndpoint are set only on a
	// Consumer-role negotiation: the identity and the DSP base URL of the
	// remote Provider.
	RemoteParticipant string `json:"remoteParticipant,omitempty"`
	ProviderEndpoint  string `json:"providerEndpoint,omitempty"`
}

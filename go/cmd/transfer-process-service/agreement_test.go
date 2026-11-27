package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/dsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEtcd replaces readEtcdValue with an in-memory map for one test.
func stubEtcd(t *testing.T, values map[string]string) {
	t.Helper()
	old := readEtcdValue
	readEtcdValue = func(key string) ([]byte, error) {
		v, ok := values[key]
		if !ok {
			return nil, nil
		}
		return []byte(v), nil
	}
	t.Cleanup(func() { readEtcdValue = old })
}

func negotiationJSON(t *testing.T, n dsp.NegotiationRecord) string {
	t.Helper()
	b, err := json.Marshal(n)
	require.NoError(t, err)
	return string(b)
}

// stubFinalizedAgreement stores one FINALIZED Provider-role negotiation of
// steward VU for participant, with id as its providerPid.
func stubFinalizedAgreement(t *testing.T, id, participant string) {
	t.Helper()
	stubEtcd(t, map[string]string{
		dsp.ProviderNegotiationKey(id): negotiationJSON(t, dsp.NegotiationRecord{
			Kind: dsp.NegotiationKindProvider, ProviderPid: id, Party: "VU", Participant: participant, State: dsp.NegotiationFinalized,
		}),
	})
}

func TestCheckAgreement(t *testing.T) {
	defer func(old string) { party = old }(party)
	party = "VU"

	const did = "did:web:consumer.example"
	finalized := dsp.NegotiationRecord{Kind: dsp.NegotiationKindProvider, ProviderPid: "urn:dynamos:negotiation:VU:1", Party: "VU", Participant: did, State: dsp.NegotiationFinalized}
	with := func(change func(*dsp.NegotiationRecord)) string {
		n := finalized
		change(&n)
		return negotiationJSON(t, n)
	}

	cases := []struct {
		name        string
		values      map[string]string
		agreementID string
		want        error
	}{
		{"by providerPid", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(*dsp.NegotiationRecord) {})}, finalized.ProviderPid, nil},
		{"by Agreement @id", map[string]string{
			dsp.AgreementIndexKey("urn:uuid:agreement-1"):     finalized.ProviderPid,
			dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(*dsp.NegotiationRecord) {}),
		}, "urn:uuid:agreement-1", nil},
		{"record without kind", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(n *dsp.NegotiationRecord) { n.Kind = "" })}, finalized.ProviderPid, nil},
		{"made-up id", map[string]string{}, "made-up", ErrAgreementNotFound},
		{"index points to nothing", map[string]string{dsp.AgreementIndexKey("urn:uuid:a"): "urn:dynamos:negotiation:VU:gone"}, "urn:uuid:a", ErrAgreementNotFound},
		{"other participant", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(n *dsp.NegotiationRecord) { n.Participant = "did:web:other" })}, finalized.ProviderPid, ErrAgreementNotFound},
		{"other steward", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(n *dsp.NegotiationRecord) { n.Party = "UVA" })}, finalized.ProviderPid, ErrAgreementNotFound},
		{"consumer kind", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(n *dsp.NegotiationRecord) { n.Kind = dsp.NegotiationKindConsumer })}, finalized.ProviderPid, ErrAgreementNotFound},
		{"not finalized", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(n *dsp.NegotiationRecord) { n.State = "AGREED" })}, finalized.ProviderPid, ErrAgreementNotFinalized},
		{"terminated", map[string]string{dsp.ProviderNegotiationKey(finalized.ProviderPid): with(func(n *dsp.NegotiationRecord) { n.State = "TERMINATED" })}, finalized.ProviderPid, ErrAgreementNotFinalized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubEtcd(t, c.values)
			err := checkAgreement(did, c.agreementID)
			if c.want == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, c.want)
			}
		})
	}
}

func TestCheckAgreement_EtcdErrorIsNotNotFound(t *testing.T) {
	old := readEtcdValue
	defer func() { readEtcdValue = old }()
	readEtcdValue = func(string) ([]byte, error) { return nil, errors.New("etcd down") }

	err := checkAgreement("did:web:x", "urn:dynamos:negotiation:VU:1")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrAgreementNotFound)
	assert.NotErrorIs(t, err, ErrAgreementNotFinalized)
}

// TestTransfersCollectionHandler_RefusesBadAgreement checks that the
// handler answers before it saves anything. store is nil in unit tests,
// so a save would panic.
func TestTransfersCollectionHandler_RefusesBadAgreement(t *testing.T) {
	defer func(old string) { party = old }(party)
	party = "VU"
	stubFinalizedAgreement(t, "urn:dynamos:negotiation:VU:1", "did:web:owner")

	cases := []struct {
		name, participant, agreementID string
		status                         int
		code                           string
	}{
		{"made-up agreement", "did:web:owner", "made-up", http.StatusNotFound, "agreement-not-found"},
		{"other participant", "jorrit.stutterheim@cloudnation.nl", "urn:dynamos:negotiation:VU:1", http.StatusNotFound, "agreement-not-found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"consumerPid":"urn:uuid:c","participant":"` + c.participant + `","agreementId":"` + c.agreementID + `","format":"HttpData-PUSH","callbackAddress":"http://callback.invalid/"}`
			rec := httptest.NewRecorder()
			transfersCollectionHandler(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/transfers", strings.NewReader(body)))

			assert.Equal(t, c.status, rec.Code)
			var ie internalError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
			assert.Equal(t, c.code, ie.Code)
		})
	}
}

func TestConsumerNegotiation(t *testing.T) {
	defer func(old string) { party = old }(party)
	party = "VU"

	const id = "urn:dynamos:negotiation:consumer:VU:1"
	good := dsp.NegotiationRecord{
		Kind: dsp.NegotiationKindConsumer, ProviderPid: "urn:dynamos:negotiation:UVA:1", ConsumerPid: id, Party: "VU",
		State: dsp.NegotiationFinalized, RemoteParticipant: "did:web:uva.example", ProviderEndpoint: "http://uva.example/api/v1",
	}
	with := func(change func(*dsp.NegotiationRecord)) map[string]string {
		n := good
		change(&n)
		return map[string]string{dsp.ConsumerNegotiationKey(id): negotiationJSON(t, n)}
	}

	cases := []struct {
		name   string
		values map[string]string
		want   error
	}{
		{"finalized", with(func(*dsp.NegotiationRecord) {}), nil},
		{"unknown", map[string]string{}, ErrAgreementNotFound},
		{"only a Provider-role record with that id", map[string]string{dsp.ProviderNegotiationKey(id): negotiationJSON(t, good)}, ErrAgreementNotFound},
		{"provider kind", with(func(n *dsp.NegotiationRecord) { n.Kind = dsp.NegotiationKindProvider }), ErrAgreementNotFound},
		{"no kind", with(func(n *dsp.NegotiationRecord) { n.Kind = "" }), ErrAgreementNotFound},
		{"other steward", with(func(n *dsp.NegotiationRecord) { n.Party = "UVA" }), ErrAgreementNotFound},
		{"no provider endpoint", with(func(n *dsp.NegotiationRecord) { n.ProviderEndpoint = "" }), ErrAgreementNotFound},
		{"no remote participant", with(func(n *dsp.NegotiationRecord) { n.RemoteParticipant = "" }), ErrAgreementNotFound},
		{"no providerPid", with(func(n *dsp.NegotiationRecord) { n.ProviderPid = "" }), ErrAgreementNotFound},
		{"not finalized", with(func(n *dsp.NegotiationRecord) { n.State = "AGREED" }), ErrAgreementNotFinalized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubEtcd(t, c.values)
			n, err := consumerNegotiation(id)
			if c.want == nil {
				require.NoError(t, err)
				assert.Equal(t, good.ProviderEndpoint, n.ProviderEndpoint)
				assert.Equal(t, good.RemoteParticipant, n.RemoteParticipant)
				assert.Equal(t, good.ProviderPid, n.ProviderPid)
			} else {
				assert.ErrorIs(t, err, c.want)
			}
		})
	}
}

// TestTransfersConsumerCollectionHandler_RefusesBadNegotiation checks that
// the handler answers before it sends anything to a Provider. The body
// names a Provider endpoint that counts calls; it must get none.
func TestTransfersConsumerCollectionHandler_RefusesBadNegotiation(t *testing.T) {
	defer func(old string) { party = old }(party)
	party = "VU"

	var calls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer provider.Close()

	const id = "urn:dynamos:negotiation:consumer:VU:1"
	stubEtcd(t, map[string]string{
		dsp.ConsumerNegotiationKey(id): negotiationJSON(t, dsp.NegotiationRecord{
			Kind: dsp.NegotiationKindConsumer, ProviderPid: "p", Party: "VU", State: "AGREED",
			RemoteParticipant: "did:web:uva.example", ProviderEndpoint: provider.URL,
		}),
	})

	cases := []struct {
		name, negotiationID string
		status              int
		code                string
	}{
		{"unknown negotiation", "made-up", http.StatusNotFound, "negotiation-not-found"},
		{"not finalized", id, http.StatusConflict, "negotiation-not-finalized"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"negotiationId":"` + c.negotiationID + `","providerEndpoint":"` + provider.URL + `","participant":"x","format":"f","callbackAddress":"http://callback.invalid/"}`
			rec := httptest.NewRecorder()
			transfersConsumerCollectionHandler(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/transfers/consumer", strings.NewReader(body)))

			assert.Equal(t, c.status, rec.Code)
			var ie internalError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ie))
			assert.Equal(t, c.code, ie.Code)
		})
	}
	assert.Zero(t, calls, "no request may reach a Provider")
}

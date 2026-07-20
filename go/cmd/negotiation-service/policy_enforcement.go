package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// odrlConstraint/odrlPermission/odrlAgreement mirror just enough of the ODRL
// Agreement shape (docs/catalog/dynamos-catalog-example.jsonld's Offer -
// Agreement carries the same permission/constraint structure once assigner/
// assignee are added) to derive an api.Relation from it. Stored opaque
// everywhere else in this service (see Negotiation.Agreement's own comment)
// - this is the one place it's actually interpreted, exactly as T2.4 scopes.
type odrlConstraint struct {
	LeftOperand  string   `json:"leftOperand"`
	RightOperand []string `json:"rightOperand"`
}

type odrlPermission struct {
	Action     string           `json:"action"`
	Constraint []odrlConstraint `json:"constraint"`
}

type odrlAgreement struct {
	Target     string           `json:"target"`
	Permission []odrlPermission `json:"permission"`
}

// dynamosVocabPrefix strips off the ODRL action/leftOperand namespace prefix
// used throughout this codebase's example messages (docs/catalog/dynamos-catalog-example.jsonld)
// - "dynamos:sqlDataRequest" -> "sqlDataRequest", matching agreements.json's
// own plain requestTypes/archetypes naming.
const dynamosVocabPrefix = "dynamos:"

// deriveRelation parses a FINALIZED negotiation's Agreement into the
// api.Relation policy-enforcer's generate_validation_response.go already
// knows how to read (issue #47's whole point - zero policy-enforcer change).
// negotiationID becomes the Relation's ID verbatim (the project owner's own
// call in the issue, not the target/offer id).
func deriveRelation(negotiationID string, agreement json.RawMessage) (api.Relation, error) {
	var a odrlAgreement
	if err := json.Unmarshal(agreement, &a); err != nil {
		return api.Relation{}, fmt.Errorf("parsing negotiated agreement: %w", err)
	}

	rel := api.Relation{ID: negotiationID}
	if a.Target != "" {
		rel.DataSets = []string{urnLastSegment(a.Target)}
	}

	seenRequestType := make(map[string]bool)
	seenArchetype := make(map[string]bool)
	seenComputeProvider := make(map[string]bool)

	for _, perm := range a.Permission {
		if rt := strings.TrimPrefix(perm.Action, dynamosVocabPrefix); rt != "" && !seenRequestType[rt] {
			seenRequestType[rt] = true
			rel.RequestTypes = append(rel.RequestTypes, rt)
		}
		for _, c := range perm.Constraint {
			switch c.LeftOperand {
			case dynamosVocabPrefix + "archetype":
				for _, v := range c.RightOperand {
					if !seenArchetype[v] {
						seenArchetype[v] = true
						rel.AllowedArchetypes = append(rel.AllowedArchetypes, v)
					}
				}
			case dynamosVocabPrefix + "computeProvider":
				for _, v := range c.RightOperand {
					if !seenComputeProvider[v] {
						seenComputeProvider[v] = true
						rel.AllowedComputeProviders = append(rel.AllowedComputeProviders, v)
					}
				}
			}
		}
	}

	return rel, nil
}

// urnLastSegment returns the trailing segment of a "urn:dynamos:dataset:VU:wageGap"
// -style identifier - "wageGap" - matching the plain dataset names
// pkg/catalog.Config.Validate checks Relation.DataSets against (agreements.json
// stores "wageGap", never the full URN).
func urnLastSegment(urn string) string {
	if i := strings.LastIndex(urn, ":"); i >= 0 {
		return urn[i+1:]
	}
	return urn
}

// writePolicyEnforcerRelation read-modify-writes Relations[participant]
// into /policyEnforcer/agreements/{party} - the exact key and shape
// policy-enforcer's generate_validation_response.go and catalog-service's
// catalog_source.go already read live. Must never blind-put: this key holds
// every other consumer's relation too (see store.go's own comment on this
// same hazard, and cmd/orchestrator/etcd_config.go's mergeAgreementRelations
// for the other writer of this key that had the identical problem).
//
// participant is an email for a non-DSP relation, or a DID for one written
// from a real DSP negotiation post-issue-#56 (see dsp-connector's
// dat_verification.go) - this map has never assumed a single identity shape,
// it's keyed by whatever string the caller was already using.
func writePolicyEnforcerRelation(etcdClient *clientv3.Client, party, participant string, rel api.Relation) error {
	key := "/policyEnforcer/agreements/" + party

	var agreement api.Agreement
	if _, err := etcd.GetAndUnmarshalJSON(etcdClient, key, &agreement); err != nil {
		return fmt.Errorf("reading policyEnforcer agreement for %q: %w", party, err)
	}
	if agreement.Name == "" {
		agreement.Name = party
	}
	if agreement.Relations == nil {
		agreement.Relations = make(map[string]api.Relation)
	}
	agreement.Relations[participant] = rel

	payload, err := json.Marshal(agreement)
	if err != nil {
		return fmt.Errorf("marshaling policyEnforcer agreement for %q: %w", party, err)
	}
	if err := etcd.PutValueToEtcd(etcdClient, key, string(payload)); err != nil {
		return fmt.Errorf("saving policyEnforcer agreement for %q: %w", party, err)
	}
	return nil
}

// applyPolicyEnforcement is negotiationEventsHandler's FINALIZED hook: derive
// the Relation from n's Agreement (set at AGREED, see negotiationAgreementHandler)
// and write it into policy-enforcer's etcd key for n's party/consumer.
func applyPolicyEnforcement(n *Negotiation) error {
	rel, err := deriveRelation(n.ProviderPid, n.Agreement)
	if err != nil {
		return err
	}
	return writePolicyEnforcerRelation(etcdClient, n.Party, n.Participant, rel)
}

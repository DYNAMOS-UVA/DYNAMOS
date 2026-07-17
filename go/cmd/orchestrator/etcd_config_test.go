package main

import (
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/stretchr/testify/assert"
)

// TestMergeAgreementRelations_KeepsExtraEtcdRelation is the core case this
// function exists for: negotiation-service (T2.4) writes a Relation into
// etcd that isn't part of the static agreements.json - a reseed must not
// drop it.
func TestMergeAgreementRelations_KeepsExtraEtcdRelation(t *testing.T) {
	fresh := api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"static@example.com": {ID: "static-id"},
		},
	}
	existing := api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"static@example.com":     {ID: "stale-static-id"},
			"negotiated@example.com": {ID: "negotiated-id"},
		},
	}

	merged := mergeAgreementRelations(fresh, existing)

	assert.Equal(t, api.Relation{ID: "static-id"}, merged.Relations["static@example.com"], "static config must stay authoritative for a key it defines")
	assert.Equal(t, api.Relation{ID: "negotiated-id"}, merged.Relations["negotiated@example.com"], "a relation only present in etcd must be carried over, not dropped")
}

// TestMergeAgreementRelations_NilFreshRelations covers a party with no
// static relations at all (e.g. RUG in agreements.json) that has since
// gained a DSP-negotiated one.
func TestMergeAgreementRelations_NilFreshRelations(t *testing.T) {
	fresh := api.Agreement{Name: "RUG"}
	existing := api.Agreement{
		Name: "RUG",
		Relations: map[string]api.Relation{
			"negotiated@example.com": {ID: "negotiated-id"},
		},
	}

	merged := mergeAgreementRelations(fresh, existing)

	assert.Equal(t, api.Relation{ID: "negotiated-id"}, merged.Relations["negotiated@example.com"])
}

// TestMergeAgreementRelations_NoExistingRelations confirms a fresh reseed of
// a brand-new key (nothing in etcd yet) round-trips unchanged - identical to
// the old blind-put behavior.
func TestMergeAgreementRelations_NoExistingRelations(t *testing.T) {
	fresh := api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"static@example.com": {ID: "static-id"},
		},
	}

	merged := mergeAgreementRelations(fresh, api.Agreement{})

	assert.Equal(t, fresh, merged)
}

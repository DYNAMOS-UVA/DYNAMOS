//go:build ignore

// tck_auto_responder simulates the autonomous provider-side policy decision
// the DSP TCK's CN (contract-negotiation, provider-role) test group assumes
// every real connector has - see wiki session notes for how this was
// discovered. DYNAMOS deliberately has no such thing: negotiation-service's
// Offer/Agreement/Finalize/Terminate provider actions only ever happen when
// something external calls its internal API (T2.2's own design - "Offer/
// Agreement are stored as-received, uninterpreted by the state machine").
// The TCK, playing consumer, waits forever for a state transition nothing
// will ever trigger on its own.
//
// This program is that external caller, but ONLY for TCK runs: it watches
// etcd's /dsp/negotiations/ prefix, and for each negotiation whose
// Offer.target dataset id matches one of the 15 CN provider tests (see
// seed_cn_datasets.go), replays that exact test's expected provider-action
// script against negotiation-service's real internal API - the same calls a
// human operator made by hand via curl throughout this project's earlier
// manual verification sessions. Never used against a real negotiation: the
// dataset ids it keys off (cn0101..cn0304) only exist because
// seed_cn_datasets.go put them there.
//
// Run alongside dsp-connector/catalog-service/negotiation-service, before
// run-tck.sh, and leave running for the duration of the TCK run.
//
// Usage: go run tck_auto_responder.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const tckFixtureDID = "did:web:localhost%3A9999"

// allCNDatasets mirrors seed_cn_datasets.go's cnTestDatasets - restoreRelation
// needs the same list to put back after a FINALIZED negotiation's
// read-modify-write (negotiation-service/policy_enforcement.go) legitimately
// replaces the whole Relation.DataSets with just that one negotiation's
// dataset. That's correct production behavior (the negotiated agreement IS
// now this identity's access), but it silently breaks every other CN test
// still queued behind it in the same run, since they all share this one
// fixture identity.
var allCNDatasets = []string{
	"wageGap",
	"cn0101", "cn0102", "cn0103", "cn0104",
	"cn0201", "cn0202", "cn0203", "cn0204", "cn0205", "cn0206", "cn0207",
	"cn0301", "cn0302", "cn0303", "cn0304",
}

const negotiationServiceURL = "http://localhost:8092"

type negotiation struct {
	ProviderPid string          `json:"providerPid"`
	State       string          `json:"state"`
	Offer       json.RawMessage `json:"offer,omitempty"`
}

type offerRef struct {
	Target string `json:"target"`
}

type step struct {
	matchState string
	action     func(id string)
}

// scripts is keyed by the dataset's short name (the last segment of the
// urn:dynamos:dataset:VU:<name> id in Offer.target) - see seed_cn_datasets.go
// and tck.properties' CN_<n>_DATASETID entries. Each script is the exact
// provider-action sequence that specific CN test expects (traced from
// eclipse-dataspacetck/dsp-tck's ContractNegotiationProvider0{1,2,3}Test.java
// and ProviderActions.java).
var scripts = map[string][]step{
	"cn0101": {{"REQUESTED", offer}},
	"cn0102": {{"REQUESTED", offer}, {"REQUESTED", terminate}}, // 2nd REQUESTED = counter-request
	"cn0103": {{"REQUESTED", offer}, {"ACCEPTED", agreement}, {"VERIFIED", finalize}},
	"cn0104": {{"REQUESTED", agreement}, {"VERIFIED", finalize}},
	"cn0201": {{"REQUESTED", terminate}},
	"cn0202": {}, // passive - consumer terminates directly, no auto action
	"cn0203": {{"REQUESTED", agreement}},
	"cn0204": {{"REQUESTED", offer}},
	"cn0205": {{"REQUESTED", offerThenTerminate}},
	"cn0206": {{"REQUESTED", offer}, {"ACCEPTED", terminate}},
	"cn0207": {{"REQUESTED", agreement}, {"VERIFIED", terminate}},
	"cn0301": {{"REQUESTED", agreement}, {"VERIFIED", finalize}},
	"cn0302": {{"REQUESTED", offer}},
	"cn0303": {{"REQUESTED", offer}},
	"cn0304": {{"REQUESTED", offer}},
}

// cursors tracks, per negotiation, which script step fires next - a script
// step only fires the first time its matchState is observed for that
// negotiation, so a repeated state (e.g. cn0102's counter-request re-entering
// REQUESTED) advances to the next step rather than re-firing the first.
var cursors = map[string]int{}

// targets caches each negotiation's dataset target (from its stored Offer),
// so offer()/agreement() can put a real "target" in the DSP messages they
// build without threading an extra parameter through every action func.
var targets = map[string]string{}

// scriptKeys caches which script a negotiation dispatches to, fixed at the
// first watch event that matched a known dataset - see handle()'s comment.
var scriptKeys = map[string]string{}

var etcdClient *clientv3.Client

func main() {
	etcdClient = etcd.GetEtcdClient("http://localhost:2379")
	defer etcdClient.Close()
	client := etcdClient

	// A reactive restoreRelation() right after finalize() isn't enough on its
	// own: the TCK's own state-polling (thenWaitForState(FINALIZED)) can see
	// the narrowed relation's effect (the state change) and move its JUnit
	// runner on to the next test before this program's own restore write
	// finishes its etcd round trip - a real, observed race, not
	// hypothetical. Keeping the relation continuously correct in the
	// background removes the race entirely instead of trying to win it.
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			restoreRelation()
		}
	}()

	log.Println("tck_auto_responder watching /dsp/negotiations/ ...")
	watch := client.Watch(context.Background(), "/dsp/negotiations/", clientv3.WithPrefix())
	for resp := range watch {
		for _, ev := range resp.Events {
			if ev.Type != clientv3.EventTypePut {
				continue
			}
			handle(ev.Kv.Value)
		}
	}
}

func handle(value []byte) {
	var n negotiation
	if err := json.Unmarshal(value, &n); err != nil {
		log.Printf("skip: %v", err)
		return
	}

	var offer offerRef
	if err := json.Unmarshal(n.Offer, &offer); err != nil || offer.Target == "" {
		return
	}
	targets[n.ProviderPid] = offer.Target

	// The dispatch key is fixed at first sight, not recomputed from the
	// current Offer every time: a counter-offer (CN:01-02/03-04) replaces
	// Offer with the TCK's own synthetic target ("ACN0102", not one of our
	// seeded cn0XXX names), which would otherwise silently drop the
	// negotiation from its script mid-run on the very step that needed it.
	datasetName, ok := scriptKeys[n.ProviderPid]
	if !ok {
		datasetName = lastSegment(offer.Target)
		if _, known := scripts[datasetName]; !known {
			return
		}
		scriptKeys[n.ProviderPid] = datasetName
	}

	script, ok := scripts[datasetName]
	if !ok {
		return
	}

	idx := cursors[n.ProviderPid]
	if idx >= len(script) {
		return
	}
	if script[idx].matchState != n.State {
		return
	}

	log.Printf("%s (%s): step %d, state=%s -> firing action", n.ProviderPid, datasetName, idx, n.State)
	cursors[n.ProviderPid] = idx + 1
	script[idx].action(n.ProviderPid)
}

func lastSegment(urn string) string {
	parts := strings.Split(urn, ":")
	return parts[len(parts)-1]
}

func offer(id string) {
	enc := urlEncode(id)
	post(enc+"/offer", map[string]any{
		"offer": map[string]any{
			"@type":      "Offer",
			"@id":        "urn:dynamos:offer:VU:tck-fixture-relation",
			"target":     targets[id],
			"permission": []map[string]any{{"action": "use"}},
		},
	})
}

func agreement(id string) {
	enc := urlEncode(id)
	post(enc+"/agreement", map[string]any{
		"agreement": map[string]any{
			"@id":      "urn:dynamos:agreement:VU:" + id,
			"@type":    "Agreement",
			"target":   targets[id],
			"assigner": "urn:dynamos:party:VU",
			"assignee": "mailto:did:web:localhost%3A9999",
			"permission": []map[string]any{{
				"action": "dynamos:sqlDataRequest",
				"constraint": []map[string]any{
					{"leftOperand": "dynamos:archetype", "operator": "isAnyOf", "rightOperand": []string{"computeToData"}},
					{"leftOperand": "dynamos:computeProvider", "operator": "isAnyOf", "rightOperand": []string{"SURF"}},
				},
			}},
		},
	})
}

func finalize(id string) {
	enc := urlEncode(id)
	post(enc+"/events", map[string]any{"eventType": "FINALIZED"})
	restoreRelation()
}

// restoreRelation puts the tck fixture identity's Relation.DataSets back to
// the full CN test list, undoing the narrowing that just happened as a side
// effect of applying policy enforcement for whichever negotiation finalized
// (see allCNDatasets' comment) - so the next queued CN test, which needs a
// different dataset, doesn't spuriously 400 on offer validation.
func restoreRelation() {
	var a api.Agreement
	if _, err := etcd.GetAndUnmarshalJSON(etcdClient, "/policyEnforcer/agreements/VU", &a); err != nil {
		log.Printf("restoreRelation: read failed: %v", err)
		return
	}
	rel := a.Relations[tckFixtureDID]
	rel.ID = "tck-fixture-relation"
	rel.DataSets = allCNDatasets
	if rel.RequestTypes == nil {
		rel.RequestTypes = []string{"sqlDataRequest", "genericRequest"}
	}
	if rel.AllowedArchetypes == nil {
		rel.AllowedArchetypes = []string{"computeToData", "dataThroughTtp"}
	}
	if rel.AllowedComputeProviders == nil {
		rel.AllowedComputeProviders = []string{"SURF"}
	}
	a.Relations[tckFixtureDID] = rel
	if err := etcd.SaveStructToEtcd(etcdClient, "/policyEnforcer/agreements/VU", a); err != nil {
		log.Printf("restoreRelation: write failed: %v", err)
	}
}

func terminate(id string) {
	enc := urlEncode(id)
	post(enc+"/termination", map[string]any{})
}

func offerThenTerminate(id string) {
	offer(id)
	time.Sleep(200 * time.Millisecond)
	terminate(id)
}

func urlEncode(id string) string {
	return strings.ReplaceAll(id, ":", "%3A")
}

func post(path string, body map[string]any) {
	raw, _ := json.Marshal(body)
	resp, err := http.Post(negotiationServiceURL+"/internal/v1/negotiations/"+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		log.Printf("POST %s failed: %v", path, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("POST %s -> %s", path, resp.Status)
	}
}

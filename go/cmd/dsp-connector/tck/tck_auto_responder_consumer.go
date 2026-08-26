//go:build ignore

// tck_auto_responder_consumer is tck_auto_responder.go's Consumer-role
// counterpart (#83). Unlike the Provider-role CN group - where real
// negotiation-service has zero autonomous behavior, so an external script
// is purely additive - Consumer-role's negotiation-service already has a
// REAL autonomous policy (#82's accept-all). Several DSP TCK CN_C tests
// send DYNAMOS the exact same message (an identical Offer or Agreement,
// same fixture content across every test) and require opposite reactions
// (accept vs counter-offer vs terminate vs do nothing) - no deterministic
// always-on policy can satisfy that, since nothing in the message itself
// says which test is running. So for a TCK run, negotiation-service's
// CONSUMER_AUTO_NEGOTIATE=false turns #82 off (main.go's own doc comment),
// and this program drives each test's real, different reaction externally
// instead - keyed by dataset id, the one thing tck.properties'
// CN_C_XX_DATASETID entries let vary per test, exactly mirroring how
// tck_auto_responder.go disambiguates CN's own per-test scripts.
//
// Run alongside dsp-connector/catalog-service/negotiation-service (with
// CONSUMER_AUTO_NEGOTIATE=false set on negotiation-service), before
// run-tck.sh, and leave running for the duration of the TCK run.
//
// Usage: go run tck_auto_responder_consumer.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const negotiationServiceURL = "http://localhost:8092"

type negotiation struct {
	ConsumerPid string          `json:"consumerPid"`
	State       string          `json:"state"`
	Offer       json.RawMessage `json:"offer,omitempty"`
	Agreement   json.RawMessage `json:"agreement,omitempty"`
}

type targetRef struct {
	Target string `json:"target"`
}

type step struct {
	matchState string
	action     func(id string)
}

// scripts is keyed by the dataset id DYNAMOS itself was told to negotiate
// for (tck.properties' CN_C_XX_DATASETID, echoed back to us in every
// Offer/Agreement's own "target" field - see targetFor's own comment).
// Each script is the exact reaction sequence that specific CN_C test
// expects, traced from eclipse-dataspacetck/dsp-tck's
// ContractNegotiationConsumer0{1,2,3}Test.java.
var scripts = map[string][]step{
	"cnc0101": {{"OFFERED", accept}, {"AGREED", verify}},
	"cnc0102": {{"OFFERED", counter}},
	"cnc0103": {{"OFFERED", terminate}},
	"cnc0104": {{"AGREED", verify}},
	"cnc0201": {}, // Provider terminates on its own, nothing to do
	"cnc0202": {{"REQUESTED", terminate}},
	"cnc0203": {{"AGREED", terminate}},
	"cnc0204": {}, // must stay at OFFERED, Provider terminates
	"cnc0205": {{"OFFERED", accept}},
	"cnc0206": {{"AGREED", verify}},
	"cnc0301": {}, // REQUESTED only, real state machine rejects the rest
	"cnc0302": {}, // must stay at OFFERED so the invalid agreement 409s
	"cnc0303": {}, // must stay at OFFERED so the invalid finalize 409s
	"cnc0304": {{"OFFERED", accept}},
	"cnc0305": {{"OFFERED", accept}},
	"cnc0306": {{"OFFERED", accept}}, // must NOT verify - AGREED stays AGREED
}

// cursors/targets/scriptKeys mirror tck_auto_responder.go's own state
// (see its comments) - per-negotiation script progress, cached dataset id,
// and the dataset id that fixed which script a negotiation dispatches to.
var cursors = map[string]int{}
var targets = map[string]string{}
var scriptKeys = map[string]string{}

var etcdClient *clientv3.Client

func main() {
	etcdClient = etcd.GetEtcdClient("http://localhost:2379")
	defer etcdClient.Close()

	log.Println("tck_auto_responder_consumer watching /dsp/negotiations/consumer/ ...")
	watch := etcdClient.Watch(context.Background(), "/dsp/negotiations/consumer/", clientv3.WithPrefix())
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

	target := targetFor(n)
	if target == "" {
		return
	}
	targets[n.ConsumerPid] = target

	datasetName, ok := scriptKeys[n.ConsumerPid]
	if !ok {
		datasetName = lastSegment(target)
		if _, known := scripts[datasetName]; !known {
			return
		}
		scriptKeys[n.ConsumerPid] = datasetName
	}

	script := scripts[datasetName]
	idx := cursors[n.ConsumerPid]
	if idx >= len(script) {
		return
	}
	if script[idx].matchState != n.State {
		return
	}

	log.Printf("%s (%s): step %d, state=%s -> firing action", n.ConsumerPid, datasetName, idx, n.State)
	cursors[n.ConsumerPid] = idx + 1
	script[idx].action(n.ConsumerPid)
}

// targetFor prefers Agreement's target over Offer's - once an Agreement
// lands, it's the current message; before that, Offer is either the real
// Provider offer (once one has arrived) or, at REQUESTED, still DYNAMOS's
// own original offer from negotiationConsumerInitiateHandler - which
// already carries the same dataset id, since that's exactly the value
// tck.properties' CN_C_XX_DATASETID feeds into the TCK's own initiate
// signal in the first place.
func targetFor(n negotiation) string {
	var ref targetRef
	if len(n.Agreement) > 0 {
		if err := json.Unmarshal(n.Agreement, &ref); err == nil && ref.Target != "" {
			return ref.Target
		}
	}
	if len(n.Offer) > 0 {
		if err := json.Unmarshal(n.Offer, &ref); err == nil && ref.Target != "" {
			return ref.Target
		}
	}
	return ""
}

func lastSegment(target string) string {
	parts := strings.Split(target, ":")
	return parts[len(parts)-1]
}

func accept(id string) {
	post(id, "accept", nil)
}

func verify(id string) {
	post(id, "verify", nil)
}

func terminate(id string) {
	post(id, "terminate", nil)
}

// counter proposes a different (but still well-formed) offer - content
// doesn't matter to the TCK's own assertion (it only checks a counter
// ContractRequestMessage arrives at all), but the shape does: @type and a
// non-empty permission array are required by the TCK's own JSON-LD schema
// validation, confirmed live (#83) the same way the initiate handler's own
// offer shape was.
func counter(id string) {
	post(id, "counter", map[string]any{
		"offer": map[string]any{
			"@id":        "urn:dynamos:offer:VU:tck-c-counter",
			"@type":      "Offer",
			"target":     targets[id],
			"permission": []map[string]any{{"action": "use"}},
		},
	})
}

func post(consumerPid, action string, body map[string]any) {
	enc := urlEncode(consumerPid)
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	} else {
		raw = []byte("{}")
	}
	resp, err := http.Post(negotiationServiceURL+"/internal/v1/negotiations/consumer/"+enc+"/"+action, "application/json", bytes.NewReader(raw))
	if err != nil {
		log.Printf("POST consumer/%s/%s failed: %v", consumerPid, action, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("POST consumer/%s/%s -> %s", consumerPid, action, resp.Status)
	}
}

func urlEncode(id string) string {
	return strings.ReplaceAll(id, ":", "%3A")
}

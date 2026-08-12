//go:build !local
// +build !local

package main

import "go.uber.org/zap"

var serviceName = "negotiation-service"
var logLevel = zap.DebugLevel
var port = ":8080"

// Set from DATA_STEWARD_NAME at startup - same convention as agent/sidecar.
var party = ""

// Same headless etcd StatefulSet address as orchestrator/policy-enforcer.
var etcdEndpoints = "http://etcd-0.etcd-headless.core.svc.cluster.local:2379,http://etcd-1.etcd-headless.core.svc.cluster.local:2379,http://etcd-2.etcd-headless.core.svc.cluster.local:2379"

// partyDAT is this negotiation-service instance's own outbound identity
// assertion, set from PARTY_DAT at startup. deliverToConsumer attaches it
// as the Authorization header on every provider-initiated push (Offer/
// Agreement/FINALIZED event/Termination) to a real Consumer's callback -
// found missing entirely, live, issue #93's demo (dsp-connector's own
// Consumer-role callback handlers DAT-verify their Authorization header;
// deliverToConsumer set none, so every real push 401'd). Never caught by
// the DSP TCK's own CN_C group: the TCK's mock consumer doesn't enforce
// this the same way a real dsp-connector does.
//
// This is a long-lived, out-of-band-minted credential (the same shape
// mint_identity.go already establishes for DYNAMOS's own outbound
// identity, see dsp-demo-issues-drafted's note on why DYNAMOS self-signs
// rather than using an MVD-issued key directly), not a fresh
// per-request-signed token - negotiation-service has no signing
// capability of its own by design (identity/DAT concerns stay in
// dsp-connector, see ADR/#56/T2.7), so this only ever holds a credential
// minted elsewhere and handed to it as config, same as any other service
// credential. Empty means "no identity to assert" - deliverToConsumer
// skips the header entirely rather than send an empty one, so a
// deployment that hasn't minted one yet degrades to today's behavior
// (works against the TCK, 401s against a real DAT-enforcing consumer)
// instead of breaking outright.
var partyDAT = ""

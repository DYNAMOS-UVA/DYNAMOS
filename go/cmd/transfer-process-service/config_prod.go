//go:build !local
// +build !local

package main

import (
	"encoding/json"

	"go.uber.org/zap"
)

var serviceName = "transfer-process-service"
var logLevel = zap.DebugLevel
var port = ":8080"

// Set from DATA_STEWARD_NAME at startup - same convention as agent/sidecar.
var party = ""

// Same headless etcd StatefulSet address as orchestrator/policy-enforcer.
var etcdEndpoints = "http://etcd-0.etcd-headless.core.svc.cluster.local:2379,http://etcd-1.etcd-headless.core.svc.cluster.local:2379,http://etcd-2.etcd-headless.core.svc.cluster.local:2379"

// api-gateway already has a real Helm chart/Service (T1.5-era). Same
// namespace-per-service convention dsp-connector's own placeholders use.
var apiGatewayURL = "http://api-gateway.api-gateway.svc.cluster.local:8080"
var apiGatewayHost = "api-gateway.api-gateway.svc.cluster.local"

// partyDAT is this transfer-process-service instance's own outbound
// identity assertion, set from PARTY_DAT at startup - same mechanism and
// same issue #93 finding as negotiation-service's own partyDAT (see its
// doc comment there for the full reasoning). deliverToConsumer attaches
// it as the Authorization header on every provider-initiated push
// (Start/Completion/Suspension/Termination) to a real Consumer's
// callback.
var partyDAT = ""

// stsTokenURL/stsClientID/stsClientSecret - see negotiation-service's own
// config_prod.go doc comment, same mechanism, same issue #94 finding.
// Empty (the default) keeps partyDAT-based delivery unchanged.
var stsTokenURL = ""
var stsClientID = ""
var stsClientSecret = ""
var stsScope = "org.eclipse.dspace.dcp.vc.type:MembershipCredential:read"

// defaultJobType/defaultJobRequest - see triggerJobAndDeliver's own doc
// comment (job_execution.go) for why this exists. Empty (the default)
// keeps a job-less transfer passive, exactly as today.
var defaultJobType = ""
var defaultJobRequest json.RawMessage = nil

// connectorBaseURL, when set, switches markStartedThenCompleted from
// sending the job result inline (DYNAMOS's own convention, which only ever
// worked DYNAMOS-to-DYNAMOS - a real external EDC consumer's strict
// TransferStartMessage validation rejects it outright, issue #94: it
// requires a real DataAddress/EDR, not the result data itself) to building
// a real DataAddress pointing at dsp-connector's own
// GET /transfers/{providerPid}/result - this instance's own externally-
// reachable base URL, mirroring dsp-connector's own connectorBaseURL
// convention (negotiation_consumer_initiate_handler.go). Empty (the
// default) keeps today's inline-result behavior unchanged - DYNAMOS-to-
// DYNAMOS demo #1 already works this way and stays untouched.
var connectorBaseURL = ""

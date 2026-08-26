//go:build local
// +build local

package main

import (
	"encoding/json"

	"go.uber.org/zap"
)

var serviceName = "transfer-process-service"
var logLevel = zap.DebugLevel
var port = ":8093"

// Overridden by DATA_STEWARD_NAME if set - default matches the VU worked example.
var party = "VU"

// Matches pf.sh's etcd port-forward (:2379), the project's actual local-dev
// convention - not orchestrator's :30005 NodePort default, which pf.sh doesn't forward.
var etcdEndpoints = "http://localhost:2379"

// api-gateway's own config_local.go has no port var: it never runs with
// -tags local, only inside the kind cluster. Reach it the same way
// project_setup's own curl example does, through pf.sh's nginx ingress
// port-forward, with a Host header for name-based routing.
var apiGatewayURL = "http://localhost:80"
var apiGatewayHost = "api-gateway.api-gateway.svc.cluster.local"

// partyDAT is this service's own outbound identity assertion - see its
// doc comment in config_prod.go. Empty by default in local dev.
var partyDAT = ""

// See config_prod.go's doc comments - empty by default, same degradation.
var stsTokenURL = ""
var stsClientID = ""
var stsClientSecret = ""
var stsScope = "org.eclipse.dspace.dcp.vc.type:MembershipCredential:read"
var defaultJobType = ""
var defaultJobRequest json.RawMessage = nil
var connectorBaseURL = ""

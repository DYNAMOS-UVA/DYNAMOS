//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "negotiation-service"
var logLevel = zap.DebugLevel
var port = ":8092"

// Overridden by DATA_STEWARD_NAME if set - default matches the VU worked example.
var party = "VU"

// Matches pf.sh's etcd port-forward (:2379), the project's actual local-dev
// convention - not orchestrator's :30005 NodePort default, which pf.sh doesn't forward.
var etcdEndpoints = "http://localhost:2379"

// partyDAT is this service's own outbound identity assertion - see its
// doc comment in config_prod.go. Empty by default in local dev; the TCK
// harness's own consumer mock doesn't require it.
var partyDAT = ""

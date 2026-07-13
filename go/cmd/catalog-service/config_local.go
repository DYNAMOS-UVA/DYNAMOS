//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "catalog-service"
var logLevel = zap.DebugLevel
var port = ":8091"

// Overridden by DATA_STEWARD_NAME if set - default matches the VU worked example.
var party = "VU"

// Same kind-cluster etcd NodePort orchestrator uses locally.
var etcdEndpoints = "http://localhost:30005"

//go:build !local
// +build !local

package main

import "go.uber.org/zap"

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

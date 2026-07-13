//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "catalog-service"
var logLevel = zap.DebugLevel
var port = ":8091"

// Same kind-cluster etcd NodePort orchestrator uses locally.
var etcdEndpoints = "http://localhost:30005"

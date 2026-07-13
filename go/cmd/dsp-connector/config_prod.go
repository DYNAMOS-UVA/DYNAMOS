//go:build !local
// +build !local

package main

import "go.uber.org/zap"

var serviceName = "dsp-connector"
var logLevel = zap.DebugLevel
var port = ":8080"

// Placeholder - catalog-service has no Helm chart/Service yet (T1.5-adjacent
// scope, not started). Assumes same namespace-per-service convention as
// api-gateway/orchestrator once it exists.
var catalogServiceURL = "http://catalog-service.catalog-service.svc.cluster.local:8080"

// apiVersion is the base path DYNAMOS publishes for this service's DSP
// catalog service endpoint. The DSP HTTPS binding only fixes what's appended
// to <base> (/catalog/request) - <base> itself is whatever DYNAMOS
// registers, so folding /api/v1 into it keeps this consistent with the
// internal convention (see api-gateway) without deviating from the spec.
var apiVersion = "/api/v1"

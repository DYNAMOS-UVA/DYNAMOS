//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "dsp-connector"
var logLevel = zap.DebugLevel
var port = ":8090"

// catalog-service's own local port (go/cmd/catalog-service/config_local.go).
var catalogServiceURL = "http://localhost:8091"

// apiVersion is the base path DYNAMOS publishes for this service's DSP
// catalog service endpoint. The DSP HTTPS binding only fixes what's appended
// to <base> (/catalog/request) - <base> itself is whatever DYNAMOS
// registers, so folding /api/v1 into it keeps this consistent with the
// internal convention (see api-gateway) without deviating from the spec.
var apiVersion = "/api/v1"

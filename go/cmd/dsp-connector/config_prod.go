//go:build !local
// +build !local

package main

import "go.uber.org/zap"

var serviceName = "dsp-connector"
var logLevel = zap.DebugLevel
var port = ":8080"

// catalogConfigPath is a placeholder until deployment wiring (e.g. a Helm
// ConfigMap mount) delivers a real config file into the container at this
// path - out of scope for issue #9, which only covers the loader itself.
var catalogConfigPath = "/app/config/catalog.json"

// apiVersion is the base path DYNAMOS publishes for this service's DSP
// catalog service endpoint. The DSP HTTPS binding only fixes what's appended
// to <base> (/catalog/request) - <base> itself is whatever DYNAMOS
// registers, so folding /api/v1 into it keeps this consistent with the
// internal convention (see api-gateway) without deviating from the spec.
var apiVersion = "/api/v1"

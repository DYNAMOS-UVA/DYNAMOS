//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "dsp-connector"
var logLevel = zap.DebugLevel
var port = ":8090"

// catalog-service's own local port (go/cmd/catalog-service/config_local.go).
var catalogServiceURL = "http://localhost:8091"

// negotiation-service's own local port (go/cmd/negotiation-service/config_local.go).
var negotiationServiceURL = "http://localhost:8092"

// transfer-process-service's own local port (go/cmd/transfer-process-service/config_local.go).
var transferServiceURL = "http://localhost:8093"

// apiVersion is the base path DYNAMOS publishes for this service's DSP
// catalog service endpoint. The DSP HTTPS binding only fixes what's appended
// to <base> (/catalog/request) - <base> itself is whatever DYNAMOS
// registers, so folding /api/v1 into it keeps this consistent with the
// internal convention (see api-gateway) without deviating from the spec.
var apiVersion = "/api/v1"

// didWebScheme: no real TLS anywhere in local dev or the TCK harness, so
// did:web resolution (dat_verification.go) uses plain http here. Same
// relaxation MVD's own local deployment makes (edc.iam.did.web.use.https:
// "false"), not a DYNAMOS-specific shortcut.
var didWebScheme = "http"

// party identifies which DYNAMOS data steward this connector instance
// belongs to - mirrors negotiation-service's own `party` config. Only
// needed once dsp-connector itself has to assert an identity as Consumer
// (#83's negotiationConsumerInitiateHandler); every other handler in this
// package only ever needs the caller's identity, never its own.
var party = "VU"

// connectorBaseURL is this dsp-connector instance's own externally
// reachable base URL, used to build the callbackAddress on an outbound
// Contract Request Message (#83's Consumer-role initiate handler). The DSP
// TCK's own Docker container reaches the host via host.docker.internal,
// the same host/port dataspacetck.dsp.connector.http.base.url already uses
// for the Provider-role groups (see tck/tck.properties).
var connectorBaseURL = "http://host.docker.internal:8090"

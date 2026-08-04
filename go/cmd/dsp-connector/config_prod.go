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

// Placeholder - negotiation-service has no Helm chart/Service yet (T2.6,
// not started). Assumes the same namespace-per-service convention
// catalog-service's own placeholder used, until T2.6 confirms the real DNS.
var negotiationServiceURL = "http://negotiation-service.negotiation-service.svc.cluster.local:8080"

// Fallback only. The real, per-party address comes from the
// TRANSFER_SERVICE_URL env var, set by charts/transfer-process-service's
// own Helm chart (T3.3). This value is never reachable on its own: no
// Service exists under this exact name, only the party-suffixed ones
// (transfer-process-service-vu, and so on).
var transferServiceURL = "http://transfer-process-service.transfer-process-service.svc.cluster.local:8080"

// apiVersion is the base path DYNAMOS publishes for this service's DSP
// catalog service endpoint. The DSP HTTPS binding only fixes what's appended
// to <base> (/catalog/request) - <base> itself is whatever DYNAMOS
// registers, so folding /api/v1 into it keeps this consistent with the
// internal convention (see api-gateway) without deviating from the spec.
var apiVersion = "/api/v1"

// didWebScheme: real did:web resolution (dat_verification.go) always uses
// https in prod, per spec. Only local/TCK builds relax this (config_local.go).
var didWebScheme = "https"

// party: overridden from the DATA_STEWARD_NAME env var in main.go - the
// same Helm-provisioned variable negotiation-service's own `party` config
// already reads (charts/dsp-connector's per-party templates set it, unused
// by this service until #83's Consumer-role initiate handler needed its own
// identity).
var party = ""

// connectorBaseURL: placeholder only, same caveat as catalogServiceURL/
// negotiationServiceURL above - no real per-party ingress hostname exists
// yet for dsp-connector to advertise as its own callbackAddress base.
var connectorBaseURL = ""

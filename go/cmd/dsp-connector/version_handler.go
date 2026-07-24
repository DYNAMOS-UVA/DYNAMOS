package main

import (
	"encoding/json"
	"net/http"
)

// versionResponse mirrors the DSP VersionResponse shape
// (specifications/common/common.protocol.md's "Exposure of Versions"
// section, schema common/protocol-version-schema.json). Every DSP-compliant
// connector MUST expose this at GET /.well-known/dspace-version -
// unversioned, unauthenticated - so a caller can discover which base path
// (`path`) this connector's actual DSP endpoints are mounted under before
// it ever calls them. The TCK's CN/TP verification suites fetch this first
// and fail every subsequent test with no real HTTP call attempted if it
// 404s, which is what happened here before this handler existed - this
// isn't specific to negotiation or transfer, it's the connector's one
// missing piece of spec-required self-description.
type versionResponse struct {
	ProtocolVersions []protocolVersion `json:"protocolVersions"`
}

type protocolVersion struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Binding string `json:"binding"`
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(versionResponse{
		ProtocolVersions: []protocolVersion{
			{Version: "2025-1", Path: apiVersion, Binding: "HTTPS"},
		},
	})
}

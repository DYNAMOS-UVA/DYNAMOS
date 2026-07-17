package main

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/lib"
)

var (
	logger = lib.InitLogger(logLevel)
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
	defer logger.Sync()

	if v := os.Getenv("CATALOG_SERVICE_URL"); v != "" {
		catalogServiceURL = v
	}
	if v := os.Getenv("NEGOTIATION_SERVICE_URL"); v != "" {
		negotiationServiceURL = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	// DSP HTTPS binding fixes /catalog/request relative to whatever <base>
	// URL DYNAMOS publishes for this service - folding apiVersion into that
	// base keeps this on the internal /api/v1 convention without deviating
	// from the spec (see the comment on apiVersion in config_local.go).
	mux.HandleFunc(apiVersion+"/catalog/request", catalogRequestHandler)
	// Dataset Request Message ack - the Catalog Protocol's second required
	// endpoint alongside /catalog/request (see catalogDatasetHandler).
	mux.HandleFunc(apiVersion+"/catalog/datasets/{id}", catalogDatasetHandler)

	// Contract Negotiation provider endpoints (T2.3, docs/negotiation/dsp-negotiation-state-machine.md).
	// "/negotiations/request" is a literal segment, so Go 1.22 ServeMux
	// matches it ahead of the "/negotiations/{providerPid}" wildcard below
	// for that exact path.
	mux.HandleFunc(apiVersion+"/negotiations/request", negotiationRequestInitHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}", negotiationGetHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/request", negotiationRequestHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/events", negotiationEventsHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/agreement/verification", negotiationVerificationHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/termination", negotiationTerminationHandler)

	logger.Sugar().Infow("Starting dsp-connector http server", "port", port, "apiVersion", apiVersion)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

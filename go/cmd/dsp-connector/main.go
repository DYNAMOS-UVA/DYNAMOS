package main

import (
	"encoding/json"
	"net/http"

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

	logger.Sugar().Infow("Starting dsp-connector http server", "port", port, "apiVersion", apiVersion)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

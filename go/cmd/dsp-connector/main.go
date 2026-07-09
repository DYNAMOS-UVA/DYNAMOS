package main

import (
	"encoding/json"
	"net/http"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
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

	logger.Sugar().Infow("Starting dsp-connector http server", "port", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

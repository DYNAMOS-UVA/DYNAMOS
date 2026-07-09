package main

import (
	"encoding/json"
	"net/http"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/catalog"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/lib"
)

var (
	logger = lib.InitLogger(logLevel)

	// catalogConfig is loaded once at startup (see loadCatalogConfig below) and
	// will be consumed by the POST /catalog/request handler added in issue #10.
	// Kept as a package-level var, not a local in main(), so that handler can
	// reach it without main() having to wire it through by hand.
	catalogConfig *catalog.Config
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// loadCatalogConfig loads the config-file-driven catalog source at startup
// (issue #9). It fails fast: an unparseable or missing config means the
// connector cannot serve a meaningful catalog, so there's nothing useful to
// do by continuing to run.
func loadCatalogConfig() *catalog.Config {
	cfg, err := catalog.LoadConfig(catalogConfigPath)
	if err != nil {
		logger.Sugar().Fatalf("Failed to load catalog config from %q: %v", catalogConfigPath, err)
	}
	logger.Sugar().Infow("Loaded catalog config", "party", cfg.Party, "datasets", len(cfg.Datasets), "relations", len(cfg.Relations))
	return cfg
}

func main() {
	defer logger.Sync()

	catalogConfig = loadCatalogConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)

	logger.Sugar().Infow("Starting dsp-connector http server", "port", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

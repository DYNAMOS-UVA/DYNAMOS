package main

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/lib"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	logger     = lib.InitLogger(logLevel)
	etcdClient *clientv3.Client
	store      *Store
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
	defer logger.Sync()

	if v := os.Getenv("DATA_STEWARD_NAME"); v != "" {
		party = v
	}
	if party == "" {
		logger.Sugar().Fatal("DATA_STEWARD_NAME not set")
	}

	if v := os.Getenv("ETCD_ENDPOINTS"); v != "" {
		etcdEndpoints = v
	}

	etcdClient = etcd.GetEtcdClient(etcdEndpoints)
	defer etcdClient.Close()

	store = NewStore(etcdClient, party)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/internal/v1/negotiations", negotiationsCollectionHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}", negotiationHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/request", negotiationRequestHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/offer", negotiationOfferHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/events", negotiationEventsHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/agreement", negotiationAgreementHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/agreement/verification", negotiationVerificationHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/termination", negotiationTerminationHandler)

	logger.Sugar().Infow("Starting negotiation-service http server", "port", port, "party", party)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

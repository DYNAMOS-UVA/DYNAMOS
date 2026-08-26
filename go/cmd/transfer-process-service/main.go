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
	if v := os.Getenv("PARTY_DAT"); v != "" {
		partyDAT = v
	}
	if v := os.Getenv("STS_TOKEN_URL"); v != "" {
		stsTokenURL = v
	}
	if v := os.Getenv("STS_CLIENT_ID"); v != "" {
		stsClientID = v
	}
	if v := os.Getenv("STS_CLIENT_SECRET"); v != "" {
		stsClientSecret = v
	}
	if v := os.Getenv("STS_SCOPE"); v != "" {
		stsScope = v
	}
	if v := os.Getenv("DEFAULT_JOB_TYPE"); v != "" {
		defaultJobType = v
	}
	if v := os.Getenv("DEFAULT_JOB_REQUEST"); v != "" {
		defaultJobRequest = json.RawMessage(v)
	}
	if v := os.Getenv("CONNECTOR_BASE_URL"); v != "" {
		connectorBaseURL = v
	}

	etcdClient = etcd.GetEtcdClient(etcdEndpoints)
	defer etcdClient.Close()

	store = NewStore(etcdClient)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/internal/v1/transfers", transfersCollectionHandler)
	mux.HandleFunc("/internal/v1/transfers/{id}", transferHandler)
	// Method-qualified ("POST /...", not "/..."): an unqualified pattern
	// counts as matching every method for Go 1.22 ServeMux's own overlap
	// check, so each of these .../{id}/<verb> routes (5 segments) would
	// otherwise conflict with the Consumer-role
	// "/internal/v1/transfers/consumer/{id}" GET route below (also 5
	// segments, "consumer" and "{id}" swap position relative to "{id}"
	// and "<verb>") - the exact class of Go 1.22 ServeMux startup panic
	// negotiation-service hit live (#83), fixed the same way there.
	mux.HandleFunc("POST /internal/v1/transfers/{id}/start", transferStartHandler)
	mux.HandleFunc("POST /internal/v1/transfers/{id}/completion", transferCompletionHandler)
	mux.HandleFunc("POST /internal/v1/transfers/{id}/suspension", transferSuspensionHandler)
	mux.HandleFunc("POST /internal/v1/transfers/{id}/termination", transferTerminationHandler)

	// Consumer-role routes (issue #93) - DYNAMOS itself plays Consumer.
	// Separate namespace, same as the etcd key split (see
	// consumerTransferKey): "consumer" is a literal path segment, more
	// specific than the {id} wildcard above, so
	// "/internal/v1/transfers/consumer" itself never collides with
	// "/internal/v1/transfers/{id}" - only the deeper
	// consumer/{id} vs {id}/<verb> pair above needed qualifying, same as
	// negotiation-service's own consumer-route registration.
	mux.HandleFunc("/internal/v1/transfers/consumer", transfersConsumerCollectionHandler)
	mux.HandleFunc("GET /internal/v1/transfers/consumer/{id}", transferConsumerHandler)
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/start", transferConsumerStartHandler)
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/completion", transferConsumerCompletionHandler)
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/suspension", transferConsumerSuspensionHandler)
	mux.HandleFunc("/internal/v1/transfers/consumer/{id}/termination", transferConsumerTerminationHandler)

	logger.Sugar().Infow("Starting transfer-process-service http server", "port", port, "party", party)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

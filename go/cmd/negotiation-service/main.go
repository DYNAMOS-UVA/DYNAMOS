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

// consumerAutoNegotiate gates #82's autonomous accept-all policy
// (autoAcceptOffer/autoVerifyAgreement in auto_accept.go) - true is the real
// production default (unconditional accept-all, what the demo runs).
// CONSUMER_AUTO_NEGOTIATE=false turns it off, leaving a Consumer-role
// negotiation sitting at OFFERED/AGREED until something explicitly drives
// it (negotiation_consumer_manual_handler.go's 4 new endpoints) - the same
// "nothing happens on its own" model Provider-role has always had. #83:
// several DSP TCK CN_C tests need DYNAMOS to react differently to the
// exact same inbound message (accept vs counter vs terminate vs stay
// put) depending on which test is running - impossible for one always-on
// deterministic policy to satisfy, so the TCK run turns accept-all off and
// drives each test's own real reaction externally instead
// (tck/tck_auto_responder_consumer.go), keyed by dataset id
// (tck.properties' CN_C_XX_DATASETID). This flag is not a shortcut to
// "pass the tests" - it's the same real internal-API surface a future real
// decision policy (the standup-discussed consortiumAgreementId work) would
// need to call anyway; accept-all is just the one caller that exists today.
var consumerAutoNegotiate = true

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
	if v := os.Getenv("CONSUMER_AUTO_NEGOTIATE"); v == "false" {
		consumerAutoNegotiate = false
	}
	if v := os.Getenv("PARTY_DAT"); v != "" {
		partyDAT = v
	}

	etcdClient = etcd.GetEtcdClient(etcdEndpoints)
	defer etcdClient.Close()

	store = NewStore(etcdClient)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/internal/v1/negotiations", negotiationsCollectionHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}", negotiationHandler)
	// Method-qualified ("POST /...", not "/..."): an unqualified pattern
	// counts as matching every method for Go 1.22 ServeMux's own overlap
	// check, so every one of these 3-segment .../{id}/<verb> routes still
	// conflicts with the Consumer-role "/internal/v1/negotiations/consumer/{id}"
	// GET route below even though the two are never actually ambiguous at
	// runtime (different methods can't both match one real request) -
	// confirmed live (#83): negotiation-service panicked on startup, one
	// pattern pair at a time, each reported as neither pattern being more
	// specific (literal "consumer" wins the {id}'s own segment, the verb
	// literal wins the next one, so overall neither dominates).
	// agreement/verification (4 segments) is unaffected - it never matches
	// the same segment count as negotiations/consumer/{id} (3), so it never
	// collided in the first place.
	mux.HandleFunc("POST /internal/v1/negotiations/{id}/request", negotiationRequestHandler)
	mux.HandleFunc("POST /internal/v1/negotiations/{id}/offer", negotiationOfferHandler)
	mux.HandleFunc("POST /internal/v1/negotiations/{id}/events", negotiationEventsHandler)
	mux.HandleFunc("POST /internal/v1/negotiations/{id}/agreement", negotiationAgreementHandler)
	mux.HandleFunc("/internal/v1/negotiations/{id}/agreement/verification", negotiationVerificationHandler)
	mux.HandleFunc("POST /internal/v1/negotiations/{id}/termination", negotiationTerminationHandler)

	// Consumer-role routes (#80) - DYNAMOS itself plays Consumer. Separate
	// namespace, same as the etcd key split (see negotiationKey): "consumer"
	// is a literal path segment, Go's ServeMux resolves it as more specific
	// than the {id} wildcard above, so these never collide with the
	// Provider-role routes even for a request literally shaped like
	// /internal/v1/negotiations/consumer - except negotiations/consumer/{id}
	// against negotiations/{id}/request, see the method-qualification note
	// on that route above.
	mux.HandleFunc("/internal/v1/negotiations/consumer", negotiationsConsumerCollectionHandler)
	mux.HandleFunc("GET /internal/v1/negotiations/consumer/{id}", negotiationConsumerHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/offer", negotiationConsumerOfferHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/agreement", negotiationConsumerAgreementHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/events", negotiationConsumerEventsHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/termination", negotiationConsumerTerminationHandler)

	// Explicit Consumer-role actions (#83) - only meaningful when
	// consumerAutoNegotiate is false (see its own doc comment above);
	// otherwise #82's accept-all already does this on its own. Same 4-segment
	// literal-verb shape as the routes directly above, no collision risk.
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/accept", negotiationConsumerAcceptHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/verify", negotiationConsumerVerifyHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/counter", negotiationConsumerCounterHandler)
	mux.HandleFunc("/internal/v1/negotiations/consumer/{id}/terminate", negotiationConsumerTerminateOutboundHandler)

	logger.Sugar().Infow("Starting negotiation-service http server", "port", port, "party", party, "consumerAutoNegotiate", consumerAutoNegotiate)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

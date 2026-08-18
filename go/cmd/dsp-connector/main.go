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
	if v := os.Getenv("TRANSFER_SERVICE_URL"); v != "" {
		transferServiceURL = v
	}
	// didWebScheme defaults to "https" in prod (config_prod.go), per the
	// did:web spec. Override to "http" only where a real deployment
	// deliberately talks to a demo identity layer with no real TLS anywhere -
	// not a relaxation of the default itself.
	if v := os.Getenv("DID_WEB_SCHEME"); v != "" {
		didWebScheme = v
	}
	if v := os.Getenv("DATA_STEWARD_NAME"); v != "" {
		party = v
	}
	if v := os.Getenv("CONNECTOR_BASE_URL"); v != "" {
		connectorBaseURL = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	// DSP version/metadata discovery endpoint - spec-required, unversioned,
	// unauthenticated (common.protocol.md's "Exposure of Versions"). The
	// spec has this at the bare root, but the DSP TCK's own MET group
	// requests it relative to whatever base URL it's configured with
	// (dataspacetck.dsp.connector.http.base.url, which is our /api/v1
	// mount) - registered at both paths so real spec-compliant discovery
	// and the TCK's own probe both resolve.
	mux.HandleFunc("/.well-known/dspace-version", versionHandler)
	mux.HandleFunc(apiVersion+"/.well-known/dspace-version", versionHandler)
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
	// "/negotiations/initiate" is DYNAMOS's own Consumer-role entry point
	// (#83, not part of the DSP spec itself) - the DSP TCK's CN_C group
	// signals a CUT to start a negotiation via a harness-specific POST to a
	// configured URL (dataspacetck.dsp.connector.negotiation.initiate.url,
	// HttpConsumerNegotiationClientImpl.initiateRequest in the TCK's own
	// source), separate from any real DSP protocol message. Same literal-
	// segment-beats-wildcard reasoning as "/negotiations/request" above.
	mux.HandleFunc(apiVersion+"/negotiations/initiate", negotiationConsumerInitiateHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}", negotiationGetHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/request", negotiationRequestHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/events", negotiationEventsHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/agreement/verification", negotiationVerificationHandler)
	mux.HandleFunc(apiVersion+"/negotiations/{providerPid}/termination", negotiationTerminationHandler)

	// Contract Negotiation Consumer Path Bindings (#81,
	// docs/negotiation/dsp-negotiation-consumer-state-machine.md) - DYNAMOS
	// itself plays Consumer here. "/callback" is a fixed literal segment,
	// not part of the DSP spec's own naming (the spec's ":callback" is
	// whatever base path a Consumer chooses and advertises via
	// callbackAddress on its own initiating Contract Request Message) - it
	// exists purely so these routes don't collide with the Provider-role
	// "{providerPid}" wildcard routes directly above, which Go's ServeMux
	// would otherwise reject as ambiguous (same wildcard shape, same
	// method, same trailing segment - e.g. both would match a literal
	// .../events). Whichever internal caller eventually starts a
	// Consumer-role negotiation (#82) must set callbackAddress to this same
	// "<this connector's own base>"+apiVersion+"/callback".
	mux.HandleFunc(apiVersion+"/callback/negotiations/{consumerPid}/offers", negotiationConsumerOffersHandler)
	mux.HandleFunc(apiVersion+"/callback/negotiations/{consumerPid}/agreement", negotiationConsumerAgreementHandler)
	mux.HandleFunc(apiVersion+"/callback/negotiations/{consumerPid}/events", negotiationConsumerEventsHandler)
	mux.HandleFunc(apiVersion+"/callback/negotiations/{consumerPid}/termination", negotiationConsumerTerminationHandler)
	// Not one of the DSP spec's own Consumer Path Bindings - the DSP TCK's
	// own verification step (#83, ConsumerNegotiationPipelineImpl's
	// thenVerifyConsumerState) polls this path to confirm the real state
	// after a scripted message exchange. Different path shape than the 4
	// routes above (no trailing segment), so no collision.
	mux.HandleFunc(apiVersion+"/callback/negotiations/{consumerPid}", negotiationConsumerGetHandler)

	// Transfer Process Consumer Path Bindings (issue #93) - DYNAMOS itself
	// plays Consumer here, same "/callback" literal-prefix reasoning as the
	// negotiation Consumer routes above. This is the handler set that
	// replaces the 2026-08-06 session's throwaway echo-listener pod: real
	// pushed data now lands on dsp-connector itself.
	mux.HandleFunc(apiVersion+"/callback/transfers/{consumerPid}/start", transferConsumerStartHandler)
	mux.HandleFunc(apiVersion+"/callback/transfers/{consumerPid}/completion", transferConsumerCompletionHandler)
	mux.HandleFunc(apiVersion+"/callback/transfers/{consumerPid}/suspension", transferConsumerSuspensionHandler)
	mux.HandleFunc(apiVersion+"/callback/transfers/{consumerPid}/termination", transferConsumerTerminationHandler)
	// Not a DSP Consumer Path Binding itself - DYNAMOS's own status/data
	// poll (see transferConsumerStatus's own doc comment). Different path
	// shape than the 4 routes above (no trailing segment), so no collision,
	// same as negotiationConsumerGetHandler's own registration above.
	mux.HandleFunc(apiVersion+"/callback/transfers/{consumerPid}", transferConsumerGetHandler)

	// Transfer Process provider endpoints (T3.1.4, docs/transfer/dsp-transfer-state-machine.md).
	// "/transfers/request" is a literal segment, so Go 1.22 ServeMux matches
	// it ahead of the "/transfers/{providerPid}" wildcard below for that
	// exact path, same as the negotiations routes above.
	mux.HandleFunc(apiVersion+"/transfers/request", transferRequestInitHandler)
	// "/transfers/initiate" is DYNAMOS's own Consumer-role entry point
	// (issue #93, not part of the DSP spec itself) - the transfer-side
	// counterpart to "/negotiations/initiate" above. Same literal-
	// segment-beats-wildcard reasoning.
	mux.HandleFunc(apiVersion+"/transfers/initiate", transferConsumerInitiateHandler)
	mux.HandleFunc(apiVersion+"/transfers/{providerPid}", transferGetHandler)
	mux.HandleFunc(apiVersion+"/transfers/{providerPid}/start", transferStartHandler)
	mux.HandleFunc(apiVersion+"/transfers/{providerPid}/completion", transferCompletionHandler)
	mux.HandleFunc(apiVersion+"/transfers/{providerPid}/termination", transferTerminationHandler)
	mux.HandleFunc(apiVersion+"/transfers/{providerPid}/suspension", transferSuspensionHandler)
	// The real HttpData-PULL endpoint (issue #94) - a genuine external
	// consumer's dataplane fetches its result from here, at the URL
	// transfer-process-service's own EDR points it at. Same
	// literal-third-segment shape as start/completion/termination/suspension
	// above, no collision risk.
	mux.HandleFunc(apiVersion+"/transfers/{providerPid}/result", transferResultHandler)

	logger.Sugar().Infow("Starting dsp-connector http server", "port", port, "apiVersion", apiVersion)
	if err := http.ListenAndServe(port, mux); err != nil {
		logger.Sugar().Fatalf("Error starting HTTP server: %v", err)
	}
}

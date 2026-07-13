package main

import (
	"fmt"
	"strings"
)

// deriveAgentEndpoint: no AgentEndpoint field exists anywhere in real
// DYNAMOS, so it's derived. Verified against charts/agents + charts/thirdparty
// Service/Ingress definitions - no "-agent" suffix, plain http, port 8080.
func deriveAgentEndpoint(party string) string {
	lower := strings.ToLower(party)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:8080/agent/v1/sqlDataRequest/%s", lower, lower, lower)
}

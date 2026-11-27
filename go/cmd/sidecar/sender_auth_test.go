package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSenderAllowed(t *testing.T) {
	allowed := []struct{ msgType, sender string }{
		{"requestApproval", "api-gateway"},
		{"validationResponse", "policy-enforcer"},
		{"policyUpdate", "policy-enforcer"},
		{"policyUpdate", "orchestrator"},
		{"compositionRequest", "orchestrator"},
		{"requestApprovalResponse", "orchestrator"},
		{"sqlDataRequest", "agent-uva"},
		{"microserviceCommunication", "agent-surf"},
		{"microserviceCommunication", "job-vu"},
	}
	for _, c := range allowed {
		assert.NoError(t, senderAllowed(c.msgType, c.sender), "%s from %s", c.msgType, c.sender)
	}

	denied := []struct{ msgType, sender string }{
		{"requestApproval", ""},                      // no user_id: an unauthenticated publish
		{"compositionRequest", "agent-uva"},          // an agent cannot start jobs at other agents
		{"requestApprovalResponse", "api-gateway"},   // the gateway cannot approve its own requests
		{"validationResponse", "orchestrator"},       // only policy-enforcer decides
		{"requestApproval", "orchestrator"},          //
		{"sqlDataRequest", "job-uva"},                // jobs only return results
		{"sqlDataRequest", "agent-"},                 // the prefix alone is no user
		{"microserviceCommunication", "api-gateway"}, //
		{"unknownType", "orchestrator"},              // no policy: deny
		{"compositionRequest", "orchestrator2"},      // exact match only
	}
	for _, c := range denied {
		err := senderAllowed(c.msgType, c.sender)
		assert.ErrorIs(t, err, errSenderNotAllowed, "%s from %q", c.msgType, c.sender)
	}
}

func TestEveryHandledTypeHasAPolicy(t *testing.T) {
	// The types the consumers in grpc_server.go and rabbit_ms_chain.go
	// handle. A type without a policy is always dropped.
	for _, typ := range []string{"validationResponse", "requestApproval", "compositionRequest",
		"sqlDataRequest", "microserviceCommunication", "policyUpdate", "requestApprovalResponse"} {
		assert.NotEmpty(t, senderPolicy[typ], typ)
	}
}

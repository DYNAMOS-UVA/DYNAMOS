package api

// Names shared by the services that mint and check job tokens for the
// legacy request flow (orchestrator issues, agents verify). Keep both
// sides on these helpers so the names cannot drift apart.

// JobIDAttribute is the token attribute that binds a token to one job.
const JobIDAttribute = "jobId"

// AgentRequestScope is the scope an agent requires for a request type, for
// example "agent:sqlDataRequest".
func AgentRequestScope(requestType string) string {
	return "agent:" + requestType
}

// GatewayRequestScope is the scope a service token needs to start a job
// through api-gateway's /requestApproval on behalf of its subject.
const GatewayRequestScope = "gateway:requestApproval"

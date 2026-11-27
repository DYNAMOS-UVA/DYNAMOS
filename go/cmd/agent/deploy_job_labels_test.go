package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobLabelsDoNotMatchAgentService(t *testing.T) {
	// charts/agents/templates/uva.yaml: the Service selects app: uva.
	labels := jobLabels("uva", "jorrit-stutterheim-1234abcd")
	_, hasApp := labels["app"]
	assert.False(t, hasApp, "a job pod must not become an endpoint of the agent Service")
	assert.Equal(t, "uva", labels["jobOf"])
}

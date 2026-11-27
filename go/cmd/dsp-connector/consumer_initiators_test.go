package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// allowInitiators sets CONSUMER_INITIATORS for one test.
func allowInitiators(t *testing.T, dids ...string) {
	t.Helper()
	old := consumerInitiators
	consumerInitiators = dids
	t.Cleanup(func() { consumerInitiators = old })
}

func TestConfigureConsumerInitiators(t *testing.T) {
	defer func(old []string) { consumerInitiators = old }(consumerInitiators)

	t.Setenv("CONSUMER_INITIATORS", "")
	configureConsumerInitiators()
	assert.False(t, mayInitiate("did:web:a"), "empty list refuses everyone")
	assert.False(t, mayInitiate(""))

	t.Setenv("CONSUMER_INITIATORS", " did:web:a , ,did:web:b%3A9999 ")
	configureConsumerInitiators()
	assert.True(t, mayInitiate("did:web:a"))
	assert.True(t, mayInitiate("did:web:b%3A9999"))
	assert.False(t, mayInitiate("did:web:c"))
	assert.False(t, mayInitiate("did:web:A"), "the match is exact")
}

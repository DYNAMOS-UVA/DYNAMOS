package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildLocalJobName_ShortIdentityUnaffected(t *testing.T) {
	got := buildLocalJobName("jorrit-stutterheim-a1b2c3d4", "vu", 1)
	assert.Equal(t, "jorrit-stutterheim-a1b2c3d4vu1", got)
	assert.LessOrEqual(t, len(got), maxK8sLabelLength)
}

// TestBuildLocalJobName_LongIdentityTruncatesUnderK8sLimit reproduces the
// real failure (issue #97, #93's live demo): a did:web identity's
// GenerateJobName output alone approaches 63 bytes before dataStewardName
// and the round counter are even appended, so the k8s API server rejected
// the resulting Job's labels ("must be no more than 63 bytes"). This test
// covers the actual identity string from that live run.
func TestBuildLocalJobName_LongIdentityTruncatesUnderK8sLimit(t *testing.T) {
	longJobName := "did-web-fixture-did-dsp-connector-svc-cluster-local-9f3456b3"
	got := buildLocalJobName(longJobName, "uva", 1)

	assert.LessOrEqual(t, len(got), maxK8sLabelLength)
	assert.True(t, strings.HasSuffix(got, "uva1"), "party+counter suffix must survive truncation intact")
}

func TestBuildLocalJobName_SuffixNeverTruncated(t *testing.T) {
	// Even a pathologically long dataStewardName+counter must not push
	// the result over budget from jobName's side - jobName degrades to
	// empty rather than the suffix itself getting cut.
	got := buildLocalJobName("short", "a-very-unusually-long-party-name-for-a-data-steward", 999999)
	assert.LessOrEqual(t, len(got), maxK8sLabelLength+len("short"), "suffix itself is never truncated, only jobName's own prefix")
	assert.True(t, strings.HasSuffix(got, "a-very-unusually-long-party-name-for-a-data-steward999999"))
}

func TestBuildLocalJobName_RoundTripsAcrossCounters(t *testing.T) {
	// Different counter values must still produce distinct names for the
	// same base jobName - this is what lets multiple job rounds route to
	// distinct k8s Jobs/queues in the first place.
	first := buildLocalJobName("base-jobname-abcdefgh", "vu", 1)
	second := buildLocalJobName("base-jobname-abcdefgh", "vu", 2)
	assert.NotEqual(t, first, second)
}

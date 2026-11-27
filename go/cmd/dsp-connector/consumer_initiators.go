package main

import (
	"os"
	"slices"
	"strings"
)

// consumerInitiators lists the participant DIDs that may call the two
// Consumer-role entry points, POST /negotiations/initiate and
// POST /transfers/initiate. These endpoints are not DSP messages: they make
// DYNAMOS itself start a negotiation or a transfer, under its own identity
// (PARTY_DAT or STS) and with any Provider. Only DYNAMOS's own operators
// may do that, so the default is an empty list: both endpoints refuse every
// caller. CONSUMER_INITIATORS sets the list (comma-separated DIDs).
var consumerInitiators []string

func configureConsumerInitiators() {
	consumerInitiators = nil
	for _, d := range strings.Split(os.Getenv("CONSUMER_INITIATORS"), ",") {
		if d = strings.TrimSpace(d); d != "" {
			consumerInitiators = append(consumerInitiators, d)
		}
	}
	if len(consumerInitiators) == 0 {
		logger.Sugar().Infow("CONSUMER_INITIATORS is empty: /negotiations/initiate and /transfers/initiate refuse every caller")
	}
}

// mayInitiate reports whether the verified participant may start a
// Consumer-role negotiation or transfer.
func mayInitiate(participant string) bool {
	return participant != "" && slices.Contains(consumerInitiators, participant)
}

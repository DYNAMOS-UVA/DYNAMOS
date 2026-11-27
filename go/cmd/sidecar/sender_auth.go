// Package main, that implements 'sidecar' functionality
//
// File: sender_auth.go
//
// Description:
// Sender authentication for RabbitMQ messages.
//
// Every sidecar connects with its own RabbitMQ user (AMQ_USER) and puts
// that user in the AMQP "user_id" property of each message it publishes.
// RabbitMQ refuses a message whose user_id is not the user of the
// connection, so on delivery user_id names the real sender.
//
// A receiving sidecar then drops each message whose sender may not send
// that message type (senderPolicy). The broker permissions (see
// scripts/auth-secrets.sh) limit which queues each user can publish to;
// this check limits what each user can say.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

// amqUser is the RabbitMQ user of this sidecar. Every published message
// carries it as user_id.
var amqUser = os.Getenv("AMQ_USER")

// senderPolicy lists, for each message type, the RabbitMQ users that may
// send it. A trailing "*" matches a prefix ("agent-*" is every agent).
//
// User names (scripts/auth-secrets.sh): orchestrator, policy-enforcer,
// api-gateway, agent-<steward> (agent sidecars), job-<steward> (the
// sidecars of the job pods that an agent starts).
var senderPolicy = map[string][]string{
	"requestApproval":           {"api-gateway"},
	"validationResponse":        {"policy-enforcer"},
	"policyUpdate":              {"policy-enforcer", "orchestrator"},
	"compositionRequest":        {"orchestrator"},
	"requestApprovalResponse":   {"orchestrator"},
	"sqlDataRequest":            {"agent-*"},
	"microserviceCommunication": {"agent-*", "job-*"},
}

// verifySenderEnv turns the check off when set to "false". Use it only
// with a local broker where every service shares one user.
const verifySenderEnv = "AMQ_VERIFY_SENDER"

var verifySender = !strings.EqualFold(strings.TrimSpace(os.Getenv(verifySenderEnv)), "false")

var errSenderNotAllowed = errors.New("sender not allowed")

// checkSender returns nil when the sender of msg may send its type.
func checkSender(msg amqp.Delivery) error {
	if !verifySender {
		return nil
	}
	return senderAllowed(msg.Type, msg.UserId)
}

func senderAllowed(msgType, sender string) error {
	if sender == "" {
		return fmt.Errorf("%w: %s message has no user_id", errSenderNotAllowed, msgType)
	}
	for _, allowed := range senderPolicy[msgType] {
		if prefix, ok := strings.CutSuffix(allowed, "*"); ok {
			if strings.HasPrefix(sender, prefix) && len(sender) > len(prefix) {
				return nil
			}
		} else if sender == allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: %q may not send %s", errSenderNotAllowed, sender, msgType)
}

// dropUnauthorized reports whether msg must be dropped, and logs why (the
// sender and type only, never the body). With manual acknowledgement it
// rejects the message without requeue, so the broker dead-letters it.
func dropUnauthorized(msg amqp.Delivery, autoAck bool) bool {
	err := checkSender(msg)
	if err == nil {
		return false
	}
	logger.Sugar().Warnw("Dropped RabbitMQ message", "reason", err.Error(), "type", msg.Type, "sender", msg.UserId, "correlationId", msg.CorrelationId)
	if !autoAck {
		if nackErr := msg.Nack(false, false); nackErr != nil {
			logger.Sugar().Warnf("Nack of dropped message failed: %v", nackErr)
		}
	}
	return true
}

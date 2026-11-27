// Package main, that implements 'sidecar' functionality
//
// File: rabbit_send.go
//
// Description:
// This file contains the server implementations of sending an AMQ message to a destination queue.
// A DYNAMOS service will package the message, and call this gRPC implementation
// on the sidecar to send the message.
//
// Notes:
//
//
// Author: Jorrit Stutterheim

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/etcd"
	pb "github.com/DYNAMOS-UVA/DYNAMOS/pkg/proto"
	bo "github.com/cenkalti/backoff/v4"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opencensus.io/trace"
	"go.opencensus.io/trace/propagation"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// This function sends a message to the specified AMQ target queue there is a retry mechanism in place
// that will retry sending the message if the target queue is not available.
//
// Parameters:
// - ctx: Context
// - message: amqp.Publishing formated message to send.
// - target: destination queue.
// - s:  serverInstance pointer, used to access the channel.
// - opts: The options to apply to the retry mechanism.
//
// Returns:
// - An empty protobuf message.
// - An error if the message could not be sent, otherwise nil.
//
// Notes:
// If the message is undeliverable, the broker returns it with a NO_ROUTE reply text and send retries.
// A message the broker refuses (for example no write permission for the routing key) is not retried.
// Everytime a message is sent the global variable running_messages is incremented, and decremented when
// the message is sent. This is to ensure that the server does not shut down while messages are still being sent.
func send(ctx context.Context, message amqp.Publishing, target string, s *serverInstance, opts ...etcd.Option) (*emptypb.Empty, error) {
	// Start with default options
	retryOpts := etcd.DefaultRetryOptions
	running_messages += 1

	// Apply any specified options
	for _, opt := range opts {
		opt(&retryOpts)
	}

	if message.Headers == nil {
		message.Headers = amqp.Table{}
	}

	sc := trace.FromContext(ctx).SpanContext()
	binarySc := propagation.Binary(sc)

	if retryOpts.AddJsonTrace {
		// Create a map to hold the span context values
		scMap := map[string]string{
			"TraceID": sc.TraceID.String(),
			"SpanID":  sc.SpanID.String(),
			// "TraceOptions": fmt.Sprintf("%02x", sc.TraceOptions.IsSampled()),
		}
		// Serialize the map to a JSON string
		scJson, err := json.Marshal(scMap)
		if err != nil {
			logger.Debug("ERRROR scJson MAP")
		}
		message.Headers["jsonTrace"] = scJson
	}

	message.Headers["binaryTrace"] = binarySc

	operation := func() error {
		// Log before sending message
		logger.Sugar().Infow("Sending message: ", "My routingKey", s.routingKey, "exchangeName", exchangeName, "target", target)

		// send runs in its own goroutine, after the gRPC call that started
		// it has returned and cancelled ctx. Keep the trace values, drop
		// the cancellation.
		timeoutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()

		err := s.publish(timeoutCtx, target, message)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errNoRoute):
			logger.Sugar().Infof("No route to job yet: %v", target)
			// This will trigger a retry
			return err
		case errors.Is(err, errRefused), errors.Is(err, errUnconfirmed):
			// Refused: a retry does not help. Unconfirmed: the message
			// can be delivered already, and a retry would duplicate it.
			logger.Sugar().Errorf("Message to %v failed: %v", target, err)
			return bo.Permanent(err)
		default:
			logger.Sugar().Debugf("Publish error: %v", err)
			return err
		}
	}

	// Create a new exponential backoff
	backoff := bo.NewExponentialBackOff()
	backoff.InitialInterval = retryOpts.InitialInterval
	backoff.MaxInterval = retryOpts.MaxInterval
	backoff.MaxElapsedTime = retryOpts.MaxElapsedTime

	err := bo.Retry(operation, backoff)
	logger.Sugar().Debugf("Backoff!!")
	if err != nil {
		logger.Sugar().Errorf("Publish failed after %v seconds, err: %s", backoff.MaxElapsedTime, err)
		running_messages -= 1
		return &emptypb.Empty{}, err
	}
	running_messages -= 1

	return &emptypb.Empty{}, nil
}

var (
	// errNoRoute: no queue is bound to the routing key yet (for example a
	// job queue that the agent did not create yet). The caller can retry.
	errNoRoute = errors.New("no route to target")
	// errRefused: the broker refused the message, for example because this
	// user may not publish to the routing key. A retry does not help.
	errRefused = errors.New("broker refused the message")
	// errUnconfirmed: the message was published, but no answer from the
	// broker came in time. It can be delivered already, so do not retry.
	errUnconfirmed = errors.New("no broker confirmation for the message")
)

// publish sends msg to routing key target and waits until the broker
// accepts it. It sets user_id to this sidecar's RabbitMQ user, which the
// broker validates and receivers check (see sender_auth.go).
//
// Each call uses its own channel in confirm mode. RabbitMQ closes a
// channel when it refuses a message, and that must not stop the consumer
// on the main channel.
func (s *serverInstance) publish(ctx context.Context, target string, msg amqp.Publishing) error {
	if s.conn == nil || s.conn.IsClosed() {
		return errors.New("no RabbitMQ connection")
	}
	msg.UserId = amqUser

	ch, err := s.conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return err
	}
	// Buffered: the client library delivers a return before the confirm
	// of the same message, so a return is in the buffer when the confirm
	// arrives.
	returns := ch.NotifyReturn(make(chan amqp.Return, 1))
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))

	if err := ch.PublishWithContext(ctx, exchangeName, target, true, false, msg); err != nil {
		return err
	}

	select {
	case r := <-returns:
		return returnError(r)
	case c := <-confirms:
		select {
		case r := <-returns:
			return returnError(r)
		default:
		}
		if !c.Ack {
			return fmt.Errorf("%w: broker did not confirm the message", errRefused)
		}
		return nil
	case e := <-closed:
		if e == nil {
			return errors.New("channel closed")
		}
		return fmt.Errorf("%w: %s", errRefused, e.Reason)
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", errUnconfirmed, ctx.Err())
	}
}

func returnError(r amqp.Return) error {
	if r.ReplyText == "NO_ROUTE" {
		return errNoRoute
	}
	return fmt.Errorf("%w: returned with %q", errRefused, r.ReplyText)
}

func (s *serverInstance) SendRequestApproval(ctx context.Context, in *pb.RequestApproval) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal requestApproval failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		Body: data,
		Type: "requestApproval",
	}

	go send(ctx, message, in.DestinationQueue, s)
	return &emptypb.Empty{}, nil
}

func (s *serverInstance) SendValidationResponse(ctx context.Context, in *pb.ValidationResponse) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal ValidationResponse failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		Body: data,
		Type: "validationResponse",
	}

	go send(ctx, message, "orchestrator-in", s)
	return &emptypb.Empty{}, nil

}

func (s *serverInstance) SendCompositionRequest(ctx context.Context, in *pb.CompositionRequest) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal CompositionRequest failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		Body: data,
		Type: "compositionRequest",
	}

	go send(ctx, message, in.DestinationQueue, s)
	return &emptypb.Empty{}, nil

}

func (s *serverInstance) SendSqlDataRequest(ctx context.Context, in *pb.SqlDataRequest) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal requestApproval failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		CorrelationId: in.RequestMetadata.CorrelationId,
		Body:          data,
		Type:          "sqlDataRequest",
	}

	logger.Sugar().Debugf("SendSqlDataRequest destination queue: %v", in.RequestMetadata.DestinationQueue)
	go send(ctx, message, in.RequestMetadata.DestinationQueue, s, etcd.WithMaxElapsedTime(10*time.Second))
	return &emptypb.Empty{}, nil

}

func (s *serverInstance) SendPolicyUpdate(ctx context.Context, in *pb.PolicyUpdate) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal PolicyUpdate failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		CorrelationId: in.RequestMetadata.CorrelationId,
		Body:          data,
		Type:          "policyUpdate",
	}

	logger.Sugar().Debugf("PolicyUpdate destination queue: %s", in.RequestMetadata.DestinationQueue)
	go send(ctx, message, in.RequestMetadata.DestinationQueue, s, etcd.WithMaxElapsedTime(10*time.Second))
	return &emptypb.Empty{}, nil

}

func (s *serverInstance) SendMicroserviceComm(ctx context.Context, in *pb.MicroserviceCommunication) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal SendMicroserviceComm failed: %s", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	message := amqp.Publishing{
		CorrelationId: in.RequestMetadata.CorrelationId,
		Body:          data,
		Type:          in.Type,
	}
	logger.Sugar().Debugf("SendMicroserviceComm destination queue: %s", in.RequestMetadata.DestinationQueue)
	go send(ctx, message, in.RequestMetadata.DestinationQueue, s, etcd.WithMaxElapsedTime(10*time.Second), etcd.WithJsonTrace())

	return &emptypb.Empty{}, nil
}

func (s *serverInstance) SendTest(ctx context.Context, in *pb.SqlDataRequest) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal SendMicroserviceComm failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		CorrelationId: "in.RequestMetadata.CorrelationId",
		Body:          data,
		Type:          "testSet",
	}
	go send(ctx, message, "no existss", s, etcd.WithMaxElapsedTime(10*time.Second))
	return &emptypb.Empty{}, nil
}

func (s *serverInstance) SendRequestApprovalResponse(ctx context.Context, in *pb.RequestApprovalResponse) (*emptypb.Empty, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		logger.Sugar().Errorf("Marshal SendRequestApprovalResponse failed: %s", err)

		return nil, status.Error(codes.Internal, err.Error())
	}

	// Do other stuff
	message := amqp.Publishing{
		Body: data,
		Type: "requestApprovalResponse",
	}

	go send(ctx, message, in.RequestMetadata.DestinationQueue, s)
	return &emptypb.Empty{}, nil
}

// Package main, that implements 'sidecar' functionality
//
// File: rabbit_setup_functions.go
//
// Description:
// This file contains functions to establish a connection to RabbitMQ,
// set up a topic exchange, queue, and consumer to listen to messages
// with the specified routing key.
//
// Notes:
//
// Author: Jorrit Stutterheim

package main

import (
	"fmt"
	"os"

	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// getAMQConnectionString returns the AMQP connection string based on the environment variables.
// It retrieves the AMQ_USER and AMQ_PASSWORD from the environment and constructs the connection string using the rabbitDNS and rabbitPort variables
// that are stored in the config files.
func getAMQConnectionString() (string, error) {
	user := os.Getenv("AMQ_USER")
	pw := os.Getenv("AMQ_PASSWORD")

	return fmt.Sprintf("amqp://%s:%s@%s:%s/", user, pw, rabbitDNS, rabbitPort), nil
}

// getConnectionToRabbitMq establishes a connection to RabbitMQ and returns the connection and channel objects.
// It retrieves the connection string from an environment variable, and creates the connection.
func getConnectionToRabbitMq() (*amqp.Connection, *amqp.Channel, error) {
	logger.Debug("Start getConnectionToRabbitMq")
	connectionString, err := getAMQConnectionString()

	if err != nil {
		logger.Sugar().Fatalw("Failed to get an AMQ connectionString: %v", err)
	}

	var conn *amqp.Connection
	var channel *amqp.Channel
	conn, channel, err = connect(connectionString)

	if err != nil {
		logger.Sugar().Fatalw("Failed to setup proper connection to RabbitMQ after 7 attempts: %v", err)
	}
	return conn, channel, nil
}

// SetupConnection establishes a connection to RabbitMQ and sets up a topic exchange, queue, and consumer to listen to
// messages with the specified routing key.
// It returns a channel to receive delivery messages, the AMQP connection, and channel objects, and an error if any occurs during the setup.
// The connection string and the routing key are passed as arguments.
// The service name in format '<name>_service' is used to declare the queue.
//
// The routingKey service.<name> is used when binding the queue to the exchange, the exchange will publish messages to all queues that match the routingkey pattern
func setupConnection(queueName string, routingKey string, queueAutoDelete bool) (<-chan amqp.Delivery, *amqp.Connection, *amqp.Channel, error) {

	conn, channel, _ := getConnectionToRabbitMq()

	err := exchange(conn, channel)
	if err != nil {
		logger.Sugar().Fatalw("Failed to create exchange: %v", err)
		return nil, nil, nil, err
	}

	queue, err := declareQueue(queueName, channel, queueAutoDelete)
	if err != nil {
		logger.Sugar().Fatalw("Failed to declare queue: %v", err)
		return nil, nil, nil, err
	}

	// Bind queue to "topic_exchange"
	// TODO: Make "topic_exchange" flexible?

	// We are going to assume the queue has been created by the composition request
	for i := 1; i <= 7; i++ { // maximum of 7 retries
		err := channel.QueueBind(
			queue.Name,       // name
			routingKey,       // key
			"topic_exchange", // exchange
			false,            // noWait
			nil,              // args
		)
		if err == nil {
			break // no error, break out of loop
		}

		if i == 7 {
			logger.Sugar().Fatalw("Queue Bind: %s", err)
			return nil, nil, nil, err
		}

		logger.Sugar().Infof("Failed to connect to QueueBind: %v", err)
		time.Sleep(2 * time.Second)
	}
	return nil, conn, channel, nil

}

// connect establishes a connection to an AMQP server using the provided connection string.
// It returns the connection and channel objects, or an error if the connection fails.
func connect(connectionString string) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(connectionString)
	if err != nil {
		return nil, nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		return nil, nil, err
	}
	return conn, channel, nil
}

// declareQueue declares a new queue with the given name, channel, and autoDelete flag.
// It returns a pointer to the declared queue and any error encountered.
func declareQueue(name string, channel *amqp.Channel, autoDelete bool) (*amqp.Queue, error) {
	queue, err := channel.QueueDeclare(
		name,       // name
		true,       // durable
		autoDelete, // delete when unused
		false,      // exclusive
		false,      // no-wait
		amqp.Table{
			"x-dead-letter-exchange": "dead-letter-exchange",
		},
	)
	if err != nil {
		return nil, err
	}
	return &queue, nil
}

// exchange makes sure the topic exchange exists.
//
// It first checks with a passive declare, which needs no permission. The
// services may not configure the exchange (that would also let them
// delete it); the broker definitions create it. Only when it does not
// exist (a local broker without definitions) does it declare it.
func exchange(conn *amqp.Connection, channel *amqp.Channel) error {
	// A failed passive declare closes its channel, so use a separate one.
	if probe, err := conn.Channel(); err == nil {
		err = probe.ExchangeDeclarePassive(exchangeName, "topic", true, false, false, false, nil)
		if err == nil {
			probe.Close()
			return nil
		}
	}
	if err := channel.ExchangeDeclare(
		exchangeName,
		"topic",
		true,  // durable
		false, // auto delete
		false, // internal
		false, // no-wait
		nil);  // arguments
	err != nil {
		return err
	}
	return nil
}

// close_channel closes the given AMQP channel and connection after all running messages have finished.
// It waits until there are no more running messages before closing the channel and connection.
func close_channel(channel *amqp.Channel, conn *amqp.Connection) {
	for {
		if running_messages == 0 {
			channel.Close()
			conn.Close()
			break
		}
		logger.Sugar().Infof("Waiting for %d messages to finish", running_messages)
		time.Sleep(1 * time.Second)
	}
}

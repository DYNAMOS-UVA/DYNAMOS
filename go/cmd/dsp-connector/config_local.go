//go:build local
// +build local

package main

import "go.uber.org/zap"

var serviceName = "dsp-connector"
var logLevel = zap.DebugLevel
var port = ":8090"

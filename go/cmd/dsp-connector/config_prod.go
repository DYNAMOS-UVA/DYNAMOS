//go:build !local
// +build !local

package main

import "go.uber.org/zap"

var serviceName = "dsp-connector"
var logLevel = zap.DebugLevel
var port = ":8080"

// catalogConfigPath is a placeholder until deployment wiring (e.g. a Helm
// ConfigMap mount) delivers a real config file into the container at this
// path - out of scope for issue #9, which only covers the loader itself.
var catalogConfigPath = "/app/config/catalog.json"

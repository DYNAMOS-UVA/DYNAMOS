package main

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	rest "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func getKubeConfig() *rest.Config {
	var config *rest.Config
	var err error

	// clientSet's package-level initializer (main.go) runs this before any
	// test in the module executes, real cluster or not - go test crashed
	// outright in CI otherwise ("KUBERNETES_SERVICE_HOST ... must be
	// defined"), for every package in the module, not just agent's own.
	// testing.Testing() (stdlib, no flag-sniffing) is true only inside a
	// test binary; no existing or planned agent test needs a live cluster
	// client, so skip building one instead of building a fake.
	if testing.Testing() {
		return nil
	}

	if local {
		// Use out-of-cluster configuration
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			logger.Sugar().Fatalf("failed to build config from Flags: %v", err)
		}
	} else {
		// Use in-cluster configuration
		config, err = rest.InClusterConfig()
		if err != nil {
			logger.Sugar().Fatalf("failed to build config inClluster: %v", err)
		}
	}

	return config
}

func getKubeClient() *kubernetes.Clientset {
	if testing.Testing() {
		return nil
	}

	config := getKubeConfig()

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create kube client: %v", err)
	}

	return clientSet
}

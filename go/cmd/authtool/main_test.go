package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth"
	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/auth/jwtauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runTool(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(args, &out, &errOut)
	return out.String(), err
}

func TestKeygenAndToken(t *testing.T) {
	dir := t.TempDir()
	_, err := runTool(t, "keygen", "-name", "orchestrator", "-out", dir)
	require.NoError(t, err)

	privPath := filepath.Join(dir, "orchestrator.key.pem")
	pubPath := filepath.Join(dir, "orchestrator.pub.pem")
	info, err := os.Stat(privPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "private key is readable by the owner only")

	_, err = runTool(t, "keygen", "-name", "orchestrator", "-out", dir)
	assert.ErrorContains(t, err, "exists", "keygen does not replace a key without -force")

	pub, err := runTool(t, "pubkey", "-key", privPath)
	require.NoError(t, err)
	onDisk, err := os.ReadFile(pubPath)
	require.NoError(t, err)
	assert.Equal(t, string(onDisk), pub)

	tok, err := runTool(t, "token", "-key", privPath, "-issuer", "dynamos-orchestrator",
		"-sub", "jorrit.stutterheim@cloudnation.nl", "-aud", "UVA",
		"-scope", "agent:sqlDataRequest", "-attr", "jobId=job-1", "-ttl", "2m")
	require.NoError(t, err)

	v, err := jwtauth.VerifierFactory(auth.Settings{
		"issuer":           "dynamos-orchestrator",
		"audience":         "UVA",
		"public_key_files": pubPath,
	})
	require.NoError(t, err)
	p, err := v.Verify(context.Background(), strings.TrimSpace(tok))
	require.NoError(t, err)
	assert.Equal(t, "jorrit.stutterheim@cloudnation.nl", p.Subject)
	assert.Equal(t, auth.KindUser, p.Kind)
	assert.True(t, p.HasScope("agent:sqlDataRequest"))
	job, _ := p.Attribute("jobId")
	assert.Equal(t, "job-1", job)
}

func TestOpenSSLKeysWork(t *testing.T) {
	// scripts/auth-secrets.sh makes the keys with openssl, not authtool.
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	priv := filepath.Join(dir, "k.key.pem")
	require.NoError(t, exec.Command("openssl", "genpkey", "-algorithm", "ed25519", "-out", priv).Run())
	pubOut, err := exec.Command("openssl", "pkey", "-in", priv, "-pubout").Output()
	require.NoError(t, err)

	pub, err := runTool(t, "pubkey", "-key", priv)
	require.NoError(t, err)
	assert.Equal(t, string(pubOut), pub)
}

func TestTokenFlagErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runTool(t, "keygen", "-name", "k", "-out", dir)
	require.NoError(t, err)
	key := filepath.Join(dir, "k.key.pem")

	cases := map[string][]string{
		"no audience": {"token", "-key", key, "-issuer", "i", "-sub", "s"},
		"no subject":  {"token", "-key", key, "-issuer", "i", "-aud", "a"},
		"no issuer":   {"token", "-key", key, "-sub", "s", "-aud", "a"},
		"bad kind":    {"token", "-key", key, "-issuer", "i", "-sub", "s", "-aud", "a", "-kind", "admin"},
		"bad attr":    {"token", "-key", key, "-issuer", "i", "-sub", "s", "-aud", "a", "-attr", "x"},
		"no key":      {"token", "-issuer", "i", "-sub", "s", "-aud", "a"},
		"bad name":    {"keygen", "-name", "../x", "-out", dir},
		"unknown":     {"rotate"},
	}
	for name, args := range cases {
		_, err := runTool(t, args...)
		assert.Error(t, err, name)
	}
}

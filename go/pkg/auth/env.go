package auth

import (
	"fmt"
	"os"
	"strings"
)

// Environment configuration.
//
// A service reads its providers from environment variables under one
// prefix, so Helm values or a Tilt config can switch auth methods without a
// code change:
//
//	<PREFIX>_PROVIDERS=oidc,jwt          verifiers, in the order Any tries them
//	<PREFIX>_PROVIDER=jwt                one issuer (IssuerFromEnv)
//	<PREFIX>_<NAME>_<SETTING>=value      one setting for provider NAME
//
// NAME is the provider name in upper case with "-" written as "_"
// (static-dev -> STATIC_DEV). SETTING becomes the lower-case settings key
// (AUTH_JWT_PUBLIC_KEY_FILES -> "public_key_files"). For example:
//
//	AUTH_PROVIDERS=jwt
//	AUTH_JWT_ISSUER=dynamos-orchestrator
//	AUTH_JWT_PUBLIC_KEY_FILES=/etc/dynamos/auth/orchestrator.pub.pem
//
// One prefix holds each provider name at most once. To use the same
// provider twice, build it in code with Registry.BuildAll.

// EnvName returns the environment-variable form of a provider name.
func EnvName(provider string) string {
	return strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
}

// SettingsFromEnviron collects the settings for provider under prefix from
// environ (os.Environ() format: "KEY=value").
func SettingsFromEnviron(prefix, provider string, environ []string) Settings {
	lead := prefix + "_" + EnvName(provider) + "_"
	s := Settings{}
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, lead) || len(key) == len(lead) {
			continue
		}
		s[strings.ToLower(strings.TrimPrefix(key, lead))] = value
	}
	return s
}

// SpecsFromEnviron reads <prefix>_PROVIDERS and the settings of every
// listed provider. It fails when the list is empty or names a provider
// twice.
func SpecsFromEnviron(prefix string, environ []string) ([]ProviderSpec, error) {
	names := listFromEnviron(prefix+"_PROVIDERS", environ)
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: %s_PROVIDERS is not set", ErrProviderConfig, prefix)
	}
	seen := make(map[string]bool, len(names))
	specs := make([]ProviderSpec, 0, len(names))
	for _, n := range names {
		if seen[n] {
			return nil, fmt.Errorf("%w: %s_PROVIDERS names %q twice", ErrProviderConfig, prefix, n)
		}
		seen[n] = true
		specs = append(specs, ProviderSpec{Name: n, Settings: SettingsFromEnviron(prefix, n, environ)})
	}
	return specs, nil
}

// VerifierFromEnv builds the verifier configured under prefix from the
// process environment, with DefaultRegistry.
func VerifierFromEnv(prefix string) (Verifier, error) {
	specs, err := SpecsFromEnviron(prefix, os.Environ())
	if err != nil {
		return nil, err
	}
	return DefaultRegistry.BuildAll(specs...)
}

// IssuerFromEnv builds the issuer named by <prefix>_PROVIDER from the
// process environment, with DefaultRegistry.
func IssuerFromEnv(prefix string) (Issuer, error) {
	return issuerFromEnviron(DefaultRegistry, prefix, os.Environ())
}

func issuerFromEnviron(r *Registry, prefix string, environ []string) (Issuer, error) {
	names := listFromEnviron(prefix+"_PROVIDER", environ)
	if len(names) != 1 {
		return nil, fmt.Errorf("%w: %s_PROVIDER must name exactly one issuer", ErrProviderConfig, prefix)
	}
	return r.BuildIssuer(names[0], SettingsFromEnviron(prefix, names[0], environ))
}

func listFromEnviron(key string, environ []string) []string {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return Settings{"v": v}.List("v")
		}
	}
	return nil
}

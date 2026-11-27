package auth

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Settings is the provider configuration, as flat string pairs so it can
// come from environment variables, Helm values or a config file without
// the core knowing any provider's schema. Each provider documents its keys.
type Settings map[string]string

// Get returns the value for key, or "" when absent.
func (s Settings) Get(key string) string { return s[key] }

// Require returns the value for key, or an ErrProviderConfig error naming
// the key when it is absent or blank.
func (s Settings) Require(key string) (string, error) {
	v := strings.TrimSpace(s[key])
	if v == "" {
		return "", fmt.Errorf("%w: setting %q is required", ErrProviderConfig, key)
	}
	return v, nil
}

// List returns the comma-separated values for key, trimmed, with empty
// items dropped. It returns nil when the key is absent or blank.
func (s Settings) List(key string) []string {
	var out []string
	for _, part := range strings.Split(s[key], ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Duration parses key as a Go duration ("30s", "5m"). It returns def when
// the key is absent or blank, and an ErrProviderConfig error when the
// value does not parse or is negative.
func (s Settings) Duration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(s[key])
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%w: setting %q must be a non-negative duration", ErrProviderConfig, key)
	}
	return d, nil
}

// Factory builds a Verifier from its Settings.
type Factory func(settings Settings) (Verifier, error)

// IssuerFactory builds an Issuer from its Settings.
type IssuerFactory func(settings Settings) (Issuer, error)

// Registry maps provider names to factories. A service builds its
// verifier by name from configuration, so adding a new auth method (for
// example for NLDT) means one new provider package and one Register call,
// with no change to handlers.
//
// Verifier and Issuer factories live in separate namespaces, so one
// provider can register both under the same name.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
	issuers   map[string]IssuerFactory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
		issuers:   make(map[string]IssuerFactory),
	}
}

// Register adds a factory under name. It fails when name is empty, the
// factory is nil, or name is already taken.
func (r *Registry) Register(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("auth: Register needs a name and a factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[name]; dup {
		return fmt.Errorf("auth: provider %q is already registered", name)
	}
	r.factories[name] = f
	return nil
}

// Build creates the Verifier registered under name.
func (r *Registry) Build(name string, settings Settings) (Verifier, error) {
	r.mu.RLock()
	f, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	if settings == nil {
		settings = Settings{}
	}
	v, err := f(settings)
	if err != nil {
		return nil, fmt.Errorf("auth: building provider %q: %w", name, err)
	}
	return v, nil
}

// RegisterIssuer adds an issuer factory under name. It fails when name is
// empty, the factory is nil, or name is already taken by another issuer.
func (r *Registry) RegisterIssuer(name string, f IssuerFactory) error {
	if name == "" || f == nil {
		return fmt.Errorf("auth: RegisterIssuer needs a name and a factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.issuers[name]; dup {
		return fmt.Errorf("auth: issuer %q is already registered", name)
	}
	r.issuers[name] = f
	return nil
}

// BuildIssuer creates the Issuer registered under name.
func (r *Registry) BuildIssuer(name string, settings Settings) (Issuer, error) {
	r.mu.RLock()
	f, ok := r.issuers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: issuer %q", ErrUnknownProvider, name)
	}
	if settings == nil {
		settings = Settings{}
	}
	i, err := f(settings)
	if err != nil {
		return nil, fmt.Errorf("auth: building issuer %q: %w", name, err)
	}
	return i, nil
}

// ProviderSpec names one provider and its settings.
type ProviderSpec struct {
	Name     string
	Settings Settings
}

// BuildAll creates every provider in specs and combines them with Any, in
// the given order. No specs is a configuration error: a service must not
// start with authentication silently disabled.
func (r *Registry) BuildAll(specs ...ProviderSpec) (Verifier, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("%w: no auth providers configured", ErrProviderConfig)
	}
	vs := make([]Verifier, 0, len(specs))
	for _, s := range specs {
		v, err := r.Build(s.Name, s.Settings)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	if len(vs) == 1 {
		return vs[0], nil
	}
	return Any(vs...), nil
}

// Names returns the registered verifier provider names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return sortedKeys(r.factories)
}

// IssuerNames returns the registered issuer names, sorted.
func (r *Registry) IssuerNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return sortedKeys(r.issuers)
}

func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// DefaultRegistry is the process-wide registry that provider packages add
// themselves to from init(), in the style of database/sql drivers.
var DefaultRegistry = NewRegistry()

// Register adds a factory to DefaultRegistry. It panics on failure,
// because it runs from init() and a duplicate name is a programming error.
func Register(name string, f Factory) {
	if err := DefaultRegistry.Register(name, f); err != nil {
		panic(err)
	}
}

// RegisterIssuer adds an issuer factory to DefaultRegistry. It panics on
// failure, for the same reason as Register.
func RegisterIssuer(name string, f IssuerFactory) {
	if err := DefaultRegistry.RegisterIssuer(name, f); err != nil {
		panic(err)
	}
}

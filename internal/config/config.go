// Package config resolves literal environment keys from an explicitly loaded
// YAML file, then the current process environment. It performs no key mapping,
// interpolation, configuration discovery, or component-specific validation.
package config

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotFound identifies a key absent from every source. Lookup errors wrap it
// with service, domain, and key identifiers, never configuration values.
var ErrNotFound = errors.New("configuration value not found")

// Resolver checks sources in order. YAML is a snapshot taken by Load; OS values
// are looked up on each call. Loaded resolvers support concurrent reads.
// The zero value contains no sources and returns ErrNotFound for all lookups.
type Resolver struct {
	sources []source
}

// source distinguishes an absent value from a present empty value and a failure.
// Future sources can precede YAML without changing the consumer-facing API.
type source interface {
	getEnvironment(service, key string) (value string, found bool, err error)
}

// Load reads exactly the supplied file and constructs YAML -> OS precedence.
// Missing, unreadable, malformed, or multi-document files are errors. An empty
// file supplies an empty YAML source. No configuration files are discovered.
// YAML null values are present-empty; ordinary scalars decode to strings.
// Unrelated service fields are ignored; values are never interpolated.
func Load(path string) (*Resolver, error) {
	yaml, err := loadYAML(path)
	if err != nil {
		return nil, err
	}
	return &Resolver{sources: []source{yaml, environmentSource{}}}, nil
}

// GetEnvironment looks up the exact service/environment/key in YAML, then the
// exact key in the process environment. Service has no meaning for OS lookup.
// Present empty values stop resolution. A source failure never falls through.
func (r *Resolver) GetEnvironment(service, key string) (string, error) {
	for _, source := range r.sources {
		value, found, err := source.getEnvironment(service, key)
		if err != nil {
			// Source errors can include values. Do not expose them through formatting or
			// unwrapping; only the requested identifiers are safe diagnostic context.
			return "", fmt.Errorf("configuration source failed: service=%q domain=%q key=%q", service, "environment", key)
		}
		if found {
			return value, nil
		}
	}
	return "", fmt.Errorf("%w: service=%q domain=%q key=%q", ErrNotFound, service, "environment", key)
}

type environmentSource struct{}

func (environmentSource) getEnvironment(_ string, key string) (string, bool, error) {
	value, found := os.LookupEnv(key)
	return value, found, nil
}

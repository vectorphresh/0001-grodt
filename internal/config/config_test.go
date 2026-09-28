package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadConfig(t *testing.T, content string) *Resolver {
	t.Helper()
	r, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertValue(t *testing.T, r *Resolver, service, key, want string) {
	t.Helper()
	got, err := r.GetEnvironment(service, key)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("resolved value differs from expected value")
	}
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	// Setenv registers restoration of the original presence and value, even when
	// the variable is subsequently unset for the duration of this test.
	t.Setenv(key, "temporary")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestYAMLResolution(t *testing.T) {
	r := loadConfig(t, `openai:
  environment:
    OPENAI_URL: http://yaml.example/v1
    OPENAI_API_KEY: yaml-key
`)
	assertValue(t, r, "openai", "OPENAI_URL", "http://yaml.example/v1")
	assertValue(t, r, "openai", "OPENAI_API_KEY", "yaml-key")
}

func TestOSFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "os-key")
	r := loadConfig(t, "openai:\n  environment:\n    OPENAI_URL: http://yaml.example/v1\n")
	assertValue(t, r, "openai", "OPENAI_API_KEY", "os-key")
}

func TestYAMLTakesPrecedence(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "os-key")
	r := loadConfig(t, "openai:\n  environment:\n    OPENAI_API_KEY: yaml-key\n")
	assertValue(t, r, "openai", "OPENAI_API_KEY", "yaml-key")
}

func TestYAMLRespectsServiceScope(t *testing.T) {
	unsetEnvironment(t, "SHARED_KEY")
	r := loadConfig(t, `openai:
  environment:
    SHARED_KEY: openai-value
alpaca:
  environment:
    SHARED_KEY: alpaca-value
`)
	assertValue(t, r, "openai", "SHARED_KEY", "openai-value")
	assertValue(t, r, "alpaca", "SHARED_KEY", "alpaca-value")
	if _, err := r.GetEnvironment("another", "SHARED_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatal("lookup searched another service")
	}
}

func TestOSHasNoServiceScope(t *testing.T) {
	t.Setenv("SHARED_KEY", "os-value")
	r := loadConfig(t, "openai: {}\nalpaca: {}\n")
	for _, service := range []string{"openai", "alpaca", "", "unrecognized"} {
		assertValue(t, r, service, "SHARED_KEY", "os-value")
	}
}

func TestPresentEmptyYAMLStopsResolution(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "os-key")
	for _, value := range []string{`""`, "null", "~", ""} {
		t.Run(fmt.Sprintf("scalar_%q", value), func(t *testing.T) {
			r := loadConfig(t, "openai:\n  environment:\n    OPENAI_API_KEY: "+value+"\n")
			assertValue(t, r, "openai", "OPENAI_API_KEY", "")
		})
	}
}

func TestPresentEmptyOSValueIsFound(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	assertValue(t, loadConfig(t, "{}"), "openai", "OPENAI_API_KEY", "")
}

func TestNotFoundIncludesIdentifiers(t *testing.T) {
	unsetEnvironment(t, "GRODT_CONFIG_MISSING")
	r := loadConfig(t, "{}")
	got, err := r.GetEnvironment("arbitrary.service", "GRODT_CONFIG_MISSING")
	if got != "" || !errors.Is(err, ErrNotFound) {
		t.Fatal("expected recognizable missing error")
	}
	for _, part := range []string{`service="arbitrary.service"`, `domain="environment"`, `key="GRODT_CONFIG_MISSING"`} {
		if !strings.Contains(err.Error(), part) {
			t.Error("error lacks lookup context")
		}
	}
}

func TestIdentifiersAreLiteral(t *testing.T) {
	for _, key := range []string{"api_key", "openai.api_key", "openai.OPENAI_API_KEY", "OPENAI_API_KEY", "Mixed_Key", "mixed_key", " key "} {
		unsetEnvironment(t, key)
	}
	r := loadConfig(t, `openai:
  environment:
    OPENAI_API_KEY: literal-value
    Mixed_Key: mixed-value
    " key ": spaces-preserved
" openai ":
  environment:
    OPENAI_API_KEY: spaced-service-value
`)
	assertValue(t, r, "openai", "OPENAI_API_KEY", "literal-value")
	assertValue(t, r, "openai", "Mixed_Key", "mixed-value")
	assertValue(t, r, "openai", " key ", "spaces-preserved")
	assertValue(t, r, " openai ", "OPENAI_API_KEY", "spaced-service-value")
	for _, key := range []string{"api_key", "openai.api_key", "openai.OPENAI_API_KEY", "mixed_key"} {
		if _, err := r.GetEnvironment("openai", key); !errors.Is(err, ErrNotFound) {
			t.Error("key was mapped or normalized")
		}
	}
	if _, err := r.GetEnvironment("OpenAI", "OPENAI_API_KEY"); !errors.Is(err, ErrNotFound) {
		t.Error("service was normalized")
	}
	t.Setenv("Mixed_Key", "exact-os-value")
	empty := loadConfig(t, "{}")
	assertValue(t, empty, "any", "Mixed_Key", "exact-os-value")
	if _, err := empty.GetEnvironment("any", "mixed_key"); !errors.Is(err, ErrNotFound) {
		t.Error("OS key was normalized")
	}
}

func TestYAMLValuesAreNotInterpolated(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "os-key")
	r := loadConfig(t, "openai:\n  environment:\n    OPENAI_API_KEY: ${OPENAI_API_KEY}\n    NUMBER: 001\n    BOOLEAN: true\n    TEXT_NULL: \"null\"\n")
	assertValue(t, r, "openai", "OPENAI_API_KEY", "${OPENAI_API_KEY}")
	assertValue(t, r, "openai", "NUMBER", "001")
	assertValue(t, r, "openai", "BOOLEAN", "true")
	assertValue(t, r, "openai", "TEXT_NULL", "null")
}

func TestUnrelatedServiceContentIsIgnored(t *testing.T) {
	unsetEnvironment(t, "SHARED_KEY")
	r := loadConfig(t, `openai:
  unrelated:
    environment:
      SHARED_KEY: nested-value
  SHARED_KEY: flattened-value
  other: [anything, true]
`)
	if _, err := r.GetEnvironment("openai", "SHARED_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatal("lookup searched outside the environment domain")
	}
}

func TestEmptyYAMLFallsBackToOS(t *testing.T) {
	t.Setenv("SHARED_KEY", "os-value")
	for _, content := range []string{"", "# comment\n", "{}", "null", "openai: {}", "openai:\n  environment: {}"} {
		assertValue(t, loadConfig(t, content), "openai", "SHARED_KEY", "os-value")
	}
}

func TestInvalidYAMLDoesNotFallBack(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "os-key")
	for _, tc := range []struct{ name, content string }{
		{"syntax", "openai: [unterminated"},
		{"root sequence", "- invalid-root"},
		{"service scalar", "openai: invalid-service"},
		{"environment sequence", "openai:\n  environment: [not-a-map]"},
		{"value sequence", "openai:\n  environment:\n    OPENAI_API_KEY: [not-a-string]"},
		{"value mapping", "openai:\n  environment:\n    OPENAI_API_KEY: {nested: value}"},
		{"duplicate key", "openai:\n  environment:\n    OPENAI_API_KEY: one\n    OPENAI_API_KEY: two"},
		{"duplicate service", "openai: {}\nopenai: {}"},
		{"multiple documents", "{}\n---\n{}"},
		{"empty second document", "{}\n---\n"},
		{"malformed second document", "{}\n---\n[unterminated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Load(writeConfig(t, tc.content))
			if err == nil || r != nil || errors.Is(err, ErrNotFound) {
				t.Fatal("invalid file did not fail loading")
			}
		})
	}
}

func TestYAMLErrorsDoNotExposeValues(t *testing.T) {
	const value = "sensitive-diagnostic-marker"
	// Duplicate keys and invalid shapes can appear in raw YAML diagnostics.
	for _, content := range []string{"openai: " + value, "openai:\n  environment:\n    " + value + ": first\n    " + value + ": second"} {
		_, err := Load(writeConfig(t, content))
		if err == nil {
			t.Fatal("expected load error")
		}
		if strings.Contains(err.Error(), value) || errors.Unwrap(err) != nil {
			t.Fatal("YAML diagnostics escaped")
		}
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "os-key")
	r, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if r != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("expected missing-file error")
	}
	if r, err := Load(""); r != nil || err == nil {
		t.Fatal("empty path triggered discovery")
	}
	if r, err := Load(t.TempDir()); r != nil || err == nil {
		t.Fatal("unreadable file accepted")
	}
}

func TestYAMLIsSnapshotAndEnvironmentIsCurrent(t *testing.T) {
	t.Setenv("SHARED_KEY", "initial-os-value")
	path := writeConfig(t, "openai:\n  environment:\n    YAML_KEY: original\n")
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertValue(t, r, "openai", "YAML_KEY", "original")
	assertValue(t, r, "openai", "SHARED_KEY", "initial-os-value")
	t.Setenv("SHARED_KEY", "updated-os-value")
	assertValue(t, r, "openai", "SHARED_KEY", "updated-os-value")
}

type sourceFunc func(string, string) (string, bool, error)

func (f sourceFunc) getEnvironment(service, key string) (string, bool, error) { return f(service, key) }

func TestSourceFailureStopsResolution(t *testing.T) {
	for _, found := range []bool{false, true} {
		called := false
		r := &Resolver{sources: []source{
			sourceFunc(func(service, key string) (string, bool, error) {
				return "sensitive-value", found, errors.New("sensitive-value")
			}),
			sourceFunc(func(service, key string) (string, bool, error) { called = true; return "fallback", true, nil }),
		}}
		value, err := r.GetEnvironment("service", "KEY")
		if err == nil || value != "" || called || errors.Is(err, ErrNotFound) {
			t.Fatal("source error was treated as a miss or success")
		}
		if strings.Contains(err.Error(), "sensitive-value") || errors.Unwrap(err) != nil {
			t.Fatal("source error leaked data")
		}
	}
}

func TestConcurrentLookups(t *testing.T) {
	t.Setenv("OS_KEY", "os-value")
	r := loadConfig(t, "service:\n  environment:\n    YAML_KEY: yaml-value")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assertValue(t, r, "service", "YAML_KEY", "yaml-value")
			assertValue(t, r, "service", "OS_KEY", "os-value")
		}()
	}
	wg.Wait()
}

func TestZeroResolverReturnsNotFound(t *testing.T) {
	var r Resolver
	if _, err := r.GetEnvironment("service", "KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatal("zero resolver must return not found")
	}
}

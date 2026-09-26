package llm

import (
	"strings"
	"testing"
	"time"
)

func TestEnvironmentOverrides(t *testing.T) {
	base := OpenAIConfig{BaseURL: "http://configured/v1", Model: "configured", APIKey: "configured-key", RequestTimeout: time.Second}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want OpenAIConfig
	}{
		{"base", nil, base},
		{"aliases", map[string]string{"OPENAI_BASE_URL": "http://alias/v1", "OPENAI_MODEL": "alias", "OPENAI_API_KEY": "alias-key"}, OpenAIConfig{BaseURL: "http://alias/v1", Model: "alias", APIKey: "alias-key", RequestTimeout: time.Second}},
		{"grodt precedence", map[string]string{"OPENAI_BASE_URL": "http://alias/v1", "OPENAI_MODEL": "alias", "OPENAI_API_KEY": "alias-key", "GRODT_LLM_BASE_URL": "http://grodt/v1", "GRODT_LLM_MODEL": "grodt", "GRODT_LLM_API_KEY": "", "GRODT_LLM_REQUEST_TIMEOUT": "3m"}, OpenAIConfig{BaseURL: "http://grodt/v1", Model: "grodt", APIKey: "", RequestTimeout: 3 * time.Minute}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := openAIConfigFromEnv(base, func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok })
			if err != nil || got != tc.want {
				t.Fatal("configuration precedence mismatch")
			}
		})
	}
	for _, value := range []string{"", "invalid", "0s", "-1s"} {
		if _, err := openAIConfigFromEnv(base, func(k string) (string, bool) { return value, k == "GRODT_LLM_REQUEST_TIMEOUT" }); err == nil {
			t.Errorf("accepted timeout %q", value)
		}
	}
}

func TestInvalidConfigurationRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*OpenAIConfig)
	}{
		{"missing model", func(c *OpenAIConfig) { c.Model = " " }},
		{"missing URL", func(c *OpenAIConfig) { c.BaseURL = "" }},
		{"relative URL", func(c *OpenAIConfig) { c.BaseURL = "localhost:8080/v1" }},
		{"URL credentials", func(c *OpenAIConfig) { c.BaseURL = "http://user:test-secret@localhost/v1" }},
		{"URL query", func(c *OpenAIConfig) { c.BaseURL = "http://localhost/v1?key=test-secret" }},
		{"URL fragment", func(c *OpenAIConfig) { c.BaseURL = "http://localhost/v1#test-secret" }},
		{"URL syntax", func(c *OpenAIConfig) { c.BaseURL = "http://%test-secret" }},
		{"wrong scheme", func(c *OpenAIConfig) { c.BaseURL = "file:///tmp/endpoint" }},
		{"missing host", func(c *OpenAIConfig) { c.BaseURL = "http:///v1" }},
		{"zero timeout", func(c *OpenAIConfig) { c.RequestTimeout = 0 }},
		{"negative timeout", func(c *OpenAIConfig) { c.RequestTimeout = -time.Second }},
		{"invalid auth header", func(c *OpenAIConfig) { c.APIKey = "test-secret\nheader" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultOpenAIConfig()
			config.Model = "model"
			tc.change(&config)
			_, err := NewOpenAIClient(config)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if strings.Contains(err.Error(), "test-secret") {
				t.Fatal("configuration error exposed secret")
			}
		})
	}
}

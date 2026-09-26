package llm

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// OpenAIConfig configures only the transport boundary. BaseURL is the API root
// (for example http://localhost:8080/v1), not the chat/completions endpoint.
type OpenAIConfig struct {
	BaseURL        string
	Model          string
	APIKey         string
	RequestTimeout time.Duration
}

func DefaultOpenAIConfig() OpenAIConfig {
	return OpenAIConfig{BaseURL: "http://localhost:8080/v1", RequestTimeout: 2 * time.Minute}
}

// OpenAIConfigFromEnv overlays configuration with environment values. GRODT_LLM_*
// takes precedence over OPENAI_* aliases. An explicitly empty key disables auth.
// A model is deliberately not selected by default.
func OpenAIConfigFromEnv(base OpenAIConfig) (OpenAIConfig, error) {
	return openAIConfigFromEnv(base, os.LookupEnv)
}

func openAIConfigFromEnv(base OpenAIConfig, lookup func(string) (string, bool)) (OpenAIConfig, error) {
	for _, item := range []struct {
		target *string
		names  []string
	}{
		{&base.BaseURL, []string{"GRODT_LLM_BASE_URL", "OPENAI_BASE_URL"}},
		{&base.Model, []string{"GRODT_LLM_MODEL", "OPENAI_MODEL"}},
		{&base.APIKey, []string{"GRODT_LLM_API_KEY", "OPENAI_API_KEY"}},
	} {
		for _, name := range item.names {
			if value, exists := lookup(name); exists {
				*item.target = value
				break
			}
		}
	}
	if value, exists := lookup("GRODT_LLM_REQUEST_TIMEOUT"); exists {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return OpenAIConfig{}, fmt.Errorf("GRODT_LLM_REQUEST_TIMEOUT must be a positive Go duration")
		}
		base.RequestTimeout = timeout
	}
	return base, nil
}

func (c OpenAIConfig) endpoint() (string, error) {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", fmt.Errorf("LLM base URL must be an absolute HTTP(S) API root")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("LLM base URL cannot contain credentials, a query, or a fragment")
	}
	if strings.TrimSpace(c.Model) == "" {
		return "", fmt.Errorf("LLM model is required")
	}
	if c.RequestTimeout <= 0 {
		return "", fmt.Errorf("LLM request timeout must be positive")
	}
	for _, ch := range c.APIKey {
		if ch < 0x20 || ch > 0x7e {
			return "", fmt.Errorf("LLM API key contains invalid header characters")
		}
	}
	return strings.TrimRight(parsed.String(), "/") + "/chat/completions", nil
}

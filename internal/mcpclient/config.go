package mcpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Config starts one stdio server directly, without a shell. Credentials may be
// inherited by name; unrelated parent environment credentials are not forwarded.
type Config struct {
	Name           string            `json:"name"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Dir            string            `json:"dir,omitempty"`
	InheritEnv     []string          `json:"inherit_env,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	StartupTimeout string            `json:"startup_timeout,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("cannot open MCP configuration")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(body) > 65536 {
		return Config{}, fmt.Errorf("cannot read MCP configuration or file exceeds 64 KiB")
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid MCP configuration JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Config{}, fmt.Errorf("expected one MCP configuration object")
	}
	if _, err := config.timeout(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) timeout() (time.Duration, error) {
	if c.Name == "" || strings.TrimSpace(c.Name) != c.Name || strings.Contains(c.Name, ".") {
		return 0, fmt.Errorf("MCP provider name must be nonempty and contain no dots or surrounding whitespace")
	}
	if strings.TrimSpace(c.Command) == "" {
		return 0, fmt.Errorf("MCP command is required")
	}
	if c.StartupTimeout == "" {
		return 30 * time.Second, nil
	}
	timeout, err := time.ParseDuration(c.StartupTimeout)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("MCP startup_timeout must be a positive Go duration")
	}
	return timeout, nil
}

func (c Config) environment() []string {
	values := map[string]string{}
	for _, name := range append([]string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SYSTEMROOT"}, c.InheritEnv...) {
		if value, ok := os.LookupEnv(name); ok {
			values[name] = value
		}
	}
	for name, value := range c.Env {
		values[name] = value
	}
	env := make([]string, 0, len(values))
	for name, value := range values {
		env = append(env, name+"="+value)
	}
	return env
}

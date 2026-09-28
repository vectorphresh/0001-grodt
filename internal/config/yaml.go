package config

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

type serviceConfig struct {
	Environment map[string]string `yaml:"environment"`
}

type yamlSource map[string]serviceConfig

func loadYAML(path string) (yamlSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration file: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var values yamlSource
	if err := decoder.Decode(&values); err != nil && err != io.EOF {
		// Parser diagnostics can quote configuration values; never retain them.
		return nil, fmt.Errorf("invalid YAML configuration in %q", path)
	}
	// Decode again because the library otherwise accepts only the first document
	// and could silently ignore invalid or additional configuration after it.
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("invalid YAML configuration in %q", path)
		}
		return nil, fmt.Errorf("configuration file %q must contain at most one YAML document", path)
	}
	return values, nil
}

func (s yamlSource) getEnvironment(service, key string) (string, bool, error) {
	value, found := s[service].Environment[key]
	return value, found, nil
}

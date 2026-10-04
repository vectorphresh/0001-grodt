package config

// StateConfig selects an existing partition manifest; it does not configure the
// module ABI or grant external execution capabilities. Relative paths are
// resolved by the application against the configuration file's directory.
type StateConfig struct {
	Definition string `yaml:"definition"`
}

func (r *Resolver) State() StateConfig {
	for _, src := range r.sources {
		if y, ok := src.(yamlSource); ok {
			return StateConfig{Definition: y["state"].Definition}
		}
	}
	return StateConfig{}
}

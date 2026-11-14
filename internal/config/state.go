package config

// StateConfig selects an existing partition manifest; it does not configure the
// module ABI or grant external execution capabilities. Relative paths are
// resolved by the application against the configuration file's directory.
type StateConfig struct {
	Definition string `yaml:"definition"`
	// Persistence optionally names a knowledge snapshot file. When set, the
	// application restores partition values from it at startup and saves the
	// snapshot again after each run, making module knowledge (including the
	// gr33d strategy context) durable across runs.
	Persistence string `yaml:"persistence"`
	// StrategyContextBytes optionally bounds the grodt_manage_strategy_context
	// payload in UTF-8 bytes. Zero selects the built-in default (8192).
	StrategyContextBytes int `yaml:"strategy_context_bytes"`
}

func (r *Resolver) State() StateConfig {
	for _, src := range r.sources {
		if y, ok := src.(yamlSource); ok {
			return StateConfig{
				Definition:           y["state"].Definition,
				Persistence:          y["state"].Persistence,
				StrategyContextBytes: y["state"].StrategyContextBytes,
			}
		}
	}
	return StateConfig{}
}

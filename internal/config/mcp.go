package config

// MCP configuration never assigns domain meaning to environment keys. Only
// explicit bindings may copy these values into an HTTP connection.
type MCPConfig struct {
	Servers []MCPServer `yaml:"servers"`
}
type MCPServer struct {
	Name        string            `yaml:"name"`
	Transport   string            `yaml:"transport"`
	URL         string            `yaml:"url"`
	Environment map[string]string `yaml:"environment"`
	HTTP        MCPHTTP           `yaml:"http"`
}
type MCPHTTP struct {
	Headers map[string]HeaderBinding `yaml:"headers"`
}
type HeaderBinding struct {
	FromEnvironment string `yaml:"from_environment"`
	Prefix          string `yaml:"prefix"`
}

// MCP returns a separate copy, preserving the loaded resolver's immutable snapshot.
func (r *Resolver) MCP() MCPConfig {
	var out MCPConfig
	for _, src := range r.sources {
		if y, ok := src.(yamlSource); ok {
			for _, s := range y["mcp"].Servers {
				c := s
				c.Environment = make(map[string]string, len(s.Environment))
				for k, v := range s.Environment {
					c.Environment[k] = v
				}
				c.HTTP.Headers = make(map[string]HeaderBinding, len(s.HTTP.Headers))
				for k, v := range s.HTTP.Headers {
					c.HTTP.Headers[k] = v
				}
				out.Servers = append(out.Servers, c)
			}
		}
	}
	return out
}

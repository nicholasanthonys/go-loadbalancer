package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so config fields can be written as YAML
// strings like "5s" or "200ms" instead of raw nanosecond integers.
// time.Duration is just an int64 under the hood, so yaml.v3 can't parse
// "5s" into it directly — this type teaches the decoder how.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

type Config struct {
	Listeners []Listener `yaml:"listeners"`
}

type Listener struct {
	Name        string      `yaml:"name"`
	Type        string      `yaml:"type"` // "l4" or "l7"
	Listen      string      `yaml:"listen"`
	Algorithm   string      `yaml:"algorithm"`
	HealthCheck HealthCheck `yaml:"health_check"`
	Backends    []Backend   `yaml:"backends"`
}

type HealthCheck struct {
	Path                string   `yaml:"path"`
	Interval            Duration `yaml:"interval"`
	Timeout             Duration `yaml:"timeout"`
	UnhealthyThreshold  int      `yaml:"unhealthy_threshold"`
	HealthyThreshold    int      `yaml:"healthy_threshold"`
}

type Backend struct {
	Addr   string `yaml:"addr"`
	Weight int    `yaml:"weight"`
}

var validAlgorithms = map[string]bool{
	"round_robin":          true,
	"weighted_round_robin": true,
	"least_conn":           true,
	"weighted_least_conn":  true,
	"random":               true,
	"power_of_two":         true,
}

// Load reads and parses the YAML file at path, applies defaults, and
// validates the result. On any error — unreadable file, malformed YAML,
// or a value that fails validation — it returns nil so a caller (see
// Store.Reload) can safely keep serving whatever config it already has
// instead of falling back to a partially-built one.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// applyDefaults fills in fields that are fine to leave out of the YAML.
// Only Weight qualifies today: an omitted weight decodes as the zero
// value, but a backend with weight 0 would never be picked by a weighted
// algorithm, which is never what an author leaving it out actually means.
func (c *Config) applyDefaults() {
	for i := range c.Listeners {
		backends := c.Listeners[i].Backends
		for j := range backends {
			if backends[j].Weight == 0 {
				backends[j].Weight = 1
			}
		}
	}
}

// Validate rejects a Config that would otherwise panic or misbehave later
// deep inside a balancer or health checker, where the error would be far
// harder to trace back to a config typo.
func (c *Config) Validate() error {
	if len(c.Listeners) == 0 {
		return fmt.Errorf("no listeners defined")
	}

	seenNames := make(map[string]bool, len(c.Listeners))
	for i, l := range c.Listeners {
		if l.Name == "" {
			return fmt.Errorf("listener[%d]: name is required", i)
		}
		if seenNames[l.Name] {
			return fmt.Errorf("listener %q: duplicate listener name", l.Name)
		}
		seenNames[l.Name] = true

		if l.Type != "l4" && l.Type != "l7" {
			return fmt.Errorf("listener %q: type must be \"l4\" or \"l7\", got %q", l.Name, l.Type)
		}
		if l.Listen == "" {
			return fmt.Errorf("listener %q: listen address is required", l.Name)
		}
		if !validAlgorithms[l.Algorithm] {
			return fmt.Errorf("listener %q: unknown algorithm %q", l.Name, l.Algorithm)
		}
		if len(l.Backends) == 0 {
			return fmt.Errorf("listener %q: at least one backend is required", l.Name)
		}
		for j, b := range l.Backends {
			if b.Addr == "" {
				return fmt.Errorf("listener %q: backend[%d]: addr is required", l.Name, j)
			}
			if b.Weight < 0 {
				return fmt.Errorf("listener %q: backend[%d]: weight cannot be negative", l.Name, j)
			}
		}

		hc := l.HealthCheck
		if hc.Interval.Duration <= 0 {
			return fmt.Errorf("listener %q: health_check.interval must be positive", l.Name)
		}
		if hc.Timeout.Duration <= 0 {
			return fmt.Errorf("listener %q: health_check.timeout must be positive", l.Name)
		}
		if hc.UnhealthyThreshold <= 0 {
			return fmt.Errorf("listener %q: health_check.unhealthy_threshold must be positive", l.Name)
		}
		if hc.HealthyThreshold <= 0 {
			return fmt.Errorf("listener %q: health_check.healthy_threshold must be positive", l.Name)
		}
		// Path only means anything for an HTTP health check; L4 pools are
		// checked with a plain TCP dial and never look at it.
		if l.Type == "l7" && hc.Path == "" {
			return fmt.Errorf("listener %q: health_check.path is required for l7 listeners", l.Name)
		}
	}

	return nil
}

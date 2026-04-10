// operator/internal/config/config.go
package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all operator tunable values.
type Config struct {
	Env            string        `yaml:"env"`             // "local" or "prod"
	Namespace      string        `yaml:"namespace"`       // "" = cluster-wide
	LeaderElection bool          `yaml:"leader_election"`
	WebhookEnabled bool          `yaml:"webhook_enabled"`
	MetricsPort    int           `yaml:"metrics_port"`
	RetryBaseDelay time.Duration `yaml:"retry_base_delay"`
	RetryMaxDelay  time.Duration `yaml:"retry_max_delay"`
	PollInterval   time.Duration `yaml:"poll_interval"`
}

// Default returns sensible defaults (local profile).
func Default() Config {
	return Config{
		Env:            "local",
		Namespace:      "aiplatform",
		LeaderElection: false,
		WebhookEnabled: false,
		MetricsPort:    8082,
		RetryBaseDelay: 5 * time.Second,
		RetryMaxDelay:  30 * time.Second,
		PollInterval:   10 * time.Second,
	}
}

// ProdDefaults returns prod profile defaults.
func ProdDefaults() Config {
	return Config{
		Env:            "prod",
		Namespace:      "",
		LeaderElection: true,
		WebhookEnabled: true,
		MetricsPort:    8082,
		RetryBaseDelay: 5 * time.Second,
		RetryMaxDelay:  5 * time.Minute,
		PollInterval:   10 * time.Second,
	}
}

// Load reads a YAML config file and merges it over the Default() config.
// If path is empty, returns the default config.
// Note: Load always starts from Default() as the merge base regardless of the
// env field in the file. Prod-specific defaults (see ProdDefaults) must be
// explicitly set in the YAML file — a sparse prod.yaml that omits a field will
// inherit the local default for that field, not the prod default.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

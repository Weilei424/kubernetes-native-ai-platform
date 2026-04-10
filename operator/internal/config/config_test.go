// operator/internal/config/config_test.go
package config

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Env != "local" {
		t.Errorf("expected Env=local, got %q", cfg.Env)
	}
	if cfg.LeaderElection {
		t.Error("expected LeaderElection=false for local default")
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("expected PollInterval=10s, got %v", cfg.PollInterval)
	}
}

func TestProdDefaults(t *testing.T) {
	cfg := ProdDefaults()
	if cfg.Env != "prod" {
		t.Errorf("expected Env=prod, got %q", cfg.Env)
	}
	if !cfg.LeaderElection {
		t.Error("expected LeaderElection=true for prod defaults")
	}
	if cfg.Namespace != "" {
		t.Errorf("expected empty Namespace for prod (cluster-wide), got %q", cfg.Namespace)
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Default()
	if cfg.Env != want.Env {
		t.Errorf("Env: got %q, want %q", cfg.Env, want.Env)
	}
	if cfg.LeaderElection != want.LeaderElection {
		t.Errorf("LeaderElection: got %v, want %v", cfg.LeaderElection, want.LeaderElection)
	}
}

func TestLoad_NonexistentPath(t *testing.T) {
	_, err := Load("/nonexistent/path/to/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent path, got nil")
	}
}

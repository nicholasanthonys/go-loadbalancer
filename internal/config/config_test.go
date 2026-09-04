package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

const validYAML = `
listeners:
  - name: web
    type: l7
    listen: ":8080"
    algorithm: least_conn
    health_check:
      path: /healthz
      interval: 5s
      timeout: 2s
      unhealthy_threshold: 3
      healthy_threshold: 2
    backends:
      - addr: "127.0.0.1:9001"
        weight: 1
      - addr: "127.0.0.1:9002"
`

func TestLoad_Valid(t *testing.T) {
	path := writeConfig(t, validYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Listeners) != 1 {
		t.Fatalf("expected 1 listener, got %d", len(cfg.Listeners))
	}
	l := cfg.Listeners[0]

	if l.HealthCheck.Interval.Duration != 5*time.Second {
		t.Errorf("expected interval 5s, got %v", l.HealthCheck.Interval.Duration)
	}
	if l.HealthCheck.Timeout.Duration != 2*time.Second {
		t.Errorf("expected timeout 2s, got %v", l.HealthCheck.Timeout.Duration)
	}

	if len(l.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(l.Backends))
	}
	// second backend omitted weight in YAML -> defaulted to 1, not 0.
	if got := l.Backends[1].Weight; got != 1 {
		t.Errorf("expected omitted weight to default to 1, got %d", got)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_RejectsUnknownAlgorithm(t *testing.T) {
	yaml := `
listeners:
  - name: web
    type: l7
    listen: ":8080"
    algorithm: bogus_algorithm
    health_check:
      path: /healthz
      interval: 5s
      timeout: 2s
      unhealthy_threshold: 3
      healthy_threshold: 2
    backends:
      - addr: "127.0.0.1:9001"
`
	path := writeConfig(t, yaml)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown algorithm, got nil")
	}
}

func TestLoad_RejectsNoBackends(t *testing.T) {
	yaml := `
listeners:
  - name: web
    type: l7
    listen: ":8080"
    algorithm: round_robin
    health_check:
      path: /healthz
      interval: 5s
      timeout: 2s
      unhealthy_threshold: 3
      healthy_threshold: 2
    backends: []
`
	path := writeConfig(t, yaml)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for empty backend list, got nil")
	}
}

func TestStore_ReloadKeepsOldConfigOnError(t *testing.T) {
	goodPath := writeConfig(t, validYAML)
	good, err := Load(goodPath)
	if err != nil {
		t.Fatalf("unexpected error loading good config: %v", err)
	}

	store := NewStore(good)

	badPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(badPath, []byte("not: [valid"), 0o644); err != nil {
		t.Fatalf("writing bad config: %v", err)
	}

	if err := store.Reload(badPath); err == nil {
		t.Fatal("expected Reload to return an error for malformed YAML")
	}

	// the bad reload must not have touched what Get returns.
	if store.Get() != good {
		t.Fatal("Reload swapped in a config despite returning an error")
	}
}

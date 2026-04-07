package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validYAML = `
github:
  repository_url: "https://github.com/test/repo"
  scale_set_name: "test-scaleset"
  runner_label: "test-runner"
  runner_group: "default"
runner:
  image: "ghcr.io/actions/actions-runner:latest"
  max_runners: 10
  work_folder: "_work"
profiles:
  high-cpu:
    cpus: "4"
    memory: "8g"
    match_patterns: ["high-cpu*"]
  low-cpu:
    cpus: "1"
    memory: "2g"
    match_patterns: ["low-cpu*"]
default_profile: "low-cpu"
proxy:
  listen_addr: ":8080"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitHub.RepositoryURL != "https://github.com/test/repo" {
		t.Errorf("got repository_url=%q", cfg.GitHub.RepositoryURL)
	}
	if cfg.DefaultProfile != "low-cpu" {
		t.Errorf("got default_profile=%q", cfg.DefaultProfile)
	}
	if len(cfg.OrderedProfiles) != 2 {
		t.Errorf("expected 2 ordered profiles, got %d", len(cfg.OrderedProfiles))
	}
	// Ordered alphabetically: high-cpu, low-cpu
	if cfg.OrderedProfiles[0].Name != "high-cpu" {
		t.Errorf("expected first profile to be high-cpu, got %q", cfg.OrderedProfiles[0].Name)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := Load(writeConfig(t, ":::invalid"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadMissingDefaultProfile(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	yaml := `
github:
  repository_url: "https://github.com/test/repo"
  scale_set_name: "test"
  runner_label: "test"
  runner_group: "default"
runner:
  image: "test:latest"
  max_runners: 5
  work_folder: "_work"
profiles:
  high-cpu:
    cpus: "4"
    memory: "8g"
    match_patterns: ["high-cpu*"]
default_profile: "nonexistent"
proxy:
  listen_addr: ":8080"
`
	_, err := Load(writeConfig(t, yaml))
	if err == nil {
		t.Fatal("expected error for invalid default_profile reference")
	}
}

func TestLoadMissingGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	_, err := Load(writeConfig(t, validYAML))
	if err == nil {
		t.Fatal("expected error for missing GITHUB_TOKEN")
	}
}

func TestLoadEmptyProfiles(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	yaml := `
github:
  repository_url: "https://github.com/test/repo"
  scale_set_name: "test"
  runner_label: "test"
  runner_group: "default"
runner:
  image: "test:latest"
  max_runners: 5
  work_folder: "_work"
profiles: {}
default_profile: "low-cpu"
proxy:
  listen_addr: ":8080"
`
	_, err := Load(writeConfig(t, yaml))
	if err == nil {
		t.Fatal("expected error for empty profiles")
	}
}

func TestLoadProfileMissingPatterns(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	yaml := `
github:
  repository_url: "https://github.com/test/repo"
  scale_set_name: "test"
  runner_label: "test"
  runner_group: "default"
runner:
  image: "test:latest"
  max_runners: 5
  work_folder: "_work"
profiles:
  high-cpu:
    cpus: "4"
    memory: "8g"
    match_patterns: []
default_profile: "high-cpu"
proxy:
  listen_addr: ":8080"
`
	_, err := Load(writeConfig(t, yaml))
	if err == nil {
		t.Fatal("expected error for profile with no patterns")
	}
}

func TestLoadAdaptiveConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")

	cfgYAML := `
github:
  repository_url: "https://github.com/test/repo"
  scale_set_name: "test-scaleset"
  runner_label: "test-label"
  runner_group: "default"
runner:
  image: "ghcr.io/actions/actions-runner:latest"
  max_runners: 5
  work_folder: "_work"
profiles:
  high-cpu:
    cpus: "4"
    memory: "8g"
    match_patterns: ["high-cpu*"]
    max_cpus: "8"
    max_memory: "16g"
  low-cpu:
    cpus: "1"
    memory: "2g"
    match_patterns: ["low-cpu*"]
default_profile: "low-cpu"
proxy:
  listen_addr: ":8080"
adaptive:
  enabled: true
  db_path: "metrics.db"
  scale_up_threshold: 0.80
  scale_down_threshold: 0.30
  scale_factor: 1.5
  history_window: 5
  max_cpus: "16"
  max_memory: "32g"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfgYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.Adaptive.Enabled {
		t.Error("expected adaptive.enabled = true")
	}
	if cfg.Adaptive.DBPath != "metrics.db" {
		t.Errorf("expected db_path = metrics.db, got %s", cfg.Adaptive.DBPath)
	}
	if cfg.Adaptive.ScaleUpThreshold != 0.80 {
		t.Errorf("expected scale_up_threshold = 0.80, got %f", cfg.Adaptive.ScaleUpThreshold)
	}
	if cfg.Adaptive.ScaleDownThreshold != 0.30 {
		t.Errorf("expected scale_down_threshold = 0.30, got %f", cfg.Adaptive.ScaleDownThreshold)
	}
	if cfg.Adaptive.ScaleFactor != 1.5 {
		t.Errorf("expected scale_factor = 1.5, got %f", cfg.Adaptive.ScaleFactor)
	}
	if cfg.Adaptive.HistoryWindow != 5 {
		t.Errorf("expected history_window = 5, got %d", cfg.Adaptive.HistoryWindow)
	}
	if cfg.Adaptive.MaxCPUs != "16" {
		t.Errorf("expected max_cpus = 16, got %s", cfg.Adaptive.MaxCPUs)
	}
	if cfg.Adaptive.MaxMemory != "32g" {
		t.Errorf("expected max_memory = 32g, got %s", cfg.Adaptive.MaxMemory)
	}

	highCPU := cfg.Profiles["high-cpu"]
	if highCPU.MaxCPUs != "8" {
		t.Errorf("expected high-cpu max_cpus = 8, got %s", highCPU.MaxCPUs)
	}
	if highCPU.MaxMemory != "16g" {
		t.Errorf("expected high-cpu max_memory = 16g, got %s", highCPU.MaxMemory)
	}

	lowCPU := cfg.Profiles["low-cpu"]
	if lowCPU.MaxCPUs != "" {
		t.Errorf("expected low-cpu max_cpus to be empty, got %s", lowCPU.MaxCPUs)
	}
}

func TestLoadAdaptiveDefaults(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")

	cfgYAML := `
github:
  repository_url: "https://github.com/test/repo"
  scale_set_name: "test-scaleset"
  runner_label: "test-label"
  runner_group: "default"
runner:
  image: "ghcr.io/actions/actions-runner:latest"
  max_runners: 5
  work_folder: "_work"
profiles:
  low-cpu:
    cpus: "1"
    memory: "2g"
    match_patterns: ["low-cpu*"]
default_profile: "low-cpu"
proxy:
  listen_addr: ":8080"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfgYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Adaptive.Enabled {
		t.Error("expected adaptive.enabled = false when section omitted")
	}
}

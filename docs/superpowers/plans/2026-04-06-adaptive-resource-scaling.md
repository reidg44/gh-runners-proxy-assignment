# Adaptive Resource Scaling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add adaptive resource scaling that monitors cgroup metrics from runner containers and adjusts CPU/memory allocations for future runs based on configurable thresholds.

**Architecture:** New `internal/metrics/` package with three components: a SQLite-backed metrics store, a cgroup collector that reads container metrics via Docker exec, and a pure-function adjuster that computes scaled resource values. The scaler wires these into its existing job lifecycle — collecting metrics at job completion and applying adjustments at provisioning time. Config gains an `adaptive` section with thresholds, scale factor, and ceilings.

**Tech Stack:** Go, SQLite via `modernc.org/sqlite` (pure Go, no CGO), Docker exec API for cgroup reads

---

### Task 1: Add SQLite dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the modernc.org/sqlite dependency**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go get modernc.org/sqlite
```

- [ ] **Step 2: Tidy modules**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go mod tidy
```

- [ ] **Step 3: Verify the dependency is in go.mod**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && grep modernc go.mod
```

Expected: Line containing `modernc.org/sqlite`

- [ ] **Step 4: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add go.mod go.sum && git commit -m "feat: add modernc.org/sqlite dependency for adaptive metrics storage"
```

---

### Task 2: Extend config with adaptive scaling settings

**Files:**
- Modify: `internal/config/config.go:12-50` (Config struct, Profile struct)
- Modify: `internal/config/config.go:72-115` (validate function)
- Modify: `config.yaml`

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAdaptiveConfig(t *testing.T) {
	// Set required env var
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

	// Verify adaptive config
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

	// Verify per-profile ceiling overrides
	highCPU := cfg.Profiles["high-cpu"]
	if highCPU.MaxCPUs != "8" {
		t.Errorf("expected high-cpu max_cpus = 8, got %s", highCPU.MaxCPUs)
	}
	if highCPU.MaxMemory != "16g" {
		t.Errorf("expected high-cpu max_memory = 16g, got %s", highCPU.MaxMemory)
	}

	// Verify low-cpu has no per-profile ceilings (empty strings)
	lowCPU := cfg.Profiles["low-cpu"]
	if lowCPU.MaxCPUs != "" {
		t.Errorf("expected low-cpu max_cpus to be empty, got %s", lowCPU.MaxCPUs)
	}
}

func TestLoadAdaptiveDefaults(t *testing.T) {
	// When adaptive section is omitted, Enabled should be false
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/config/... -v -run TestLoadAdaptive
```

Expected: Compilation error — `cfg.Adaptive` field does not exist.

- [ ] **Step 3: Add Adaptive struct and per-profile ceiling fields**

In `internal/config/config.go`, add the `AdaptiveConfig` struct after `ProxyConfig` (after line 44):

```go
// AdaptiveConfig controls automatic resource scaling based on observed usage.
type AdaptiveConfig struct {
	Enabled            bool    `yaml:"enabled"`
	DBPath             string  `yaml:"db_path"`
	ScaleUpThreshold   float64 `yaml:"scale_up_threshold"`
	ScaleDownThreshold float64 `yaml:"scale_down_threshold"`
	ScaleFactor        float64 `yaml:"scale_factor"`
	HistoryWindow      int     `yaml:"history_window"`
	MaxCPUs            string  `yaml:"max_cpus"`
	MaxMemory          string  `yaml:"max_memory"`
}
```

Add `Adaptive AdaptiveConfig` field to the `Config` struct (after the `Proxy` field, line 17):

```go
Adaptive       AdaptiveConfig      `yaml:"adaptive"`
```

Add `MaxCPUs` and `MaxMemory` fields to the `Profile` struct (after `MatchPatterns`, line 39):

```go
MaxCPUs       string   `yaml:"max_cpus"`
MaxMemory     string   `yaml:"max_memory"`
```

Add validation for adaptive config inside the `validate()` method. After the `GITHUB_TOKEN` check (after line 113), add:

```go
if c.Adaptive.Enabled {
	if c.Adaptive.ScaleUpThreshold <= 0 || c.Adaptive.ScaleUpThreshold > 1 {
		return fmt.Errorf("adaptive.scale_up_threshold must be between 0 and 1")
	}
	if c.Adaptive.ScaleDownThreshold < 0 || c.Adaptive.ScaleDownThreshold >= c.Adaptive.ScaleUpThreshold {
		return fmt.Errorf("adaptive.scale_down_threshold must be >= 0 and less than scale_up_threshold")
	}
	if c.Adaptive.ScaleFactor <= 1 {
		return fmt.Errorf("adaptive.scale_factor must be greater than 1")
	}
	if c.Adaptive.HistoryWindow <= 0 {
		return fmt.Errorf("adaptive.history_window must be positive")
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/config/... -v -run TestLoadAdaptive
```

Expected: Both `TestLoadAdaptiveConfig` and `TestLoadAdaptiveDefaults` PASS.

- [ ] **Step 5: Update config.yaml with adaptive section**

Add to the end of `config.yaml`:

```yaml

adaptive:
  enabled: true
  db_path: "metrics.db"
  scale_up_threshold: 0.80
  scale_down_threshold: 0.30
  scale_factor: 1.5
  history_window: 5
  max_cpus: "16"
  max_memory: "32g"
```

- [ ] **Step 6: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add internal/config/config.go internal/config/config_test.go config.yaml && git commit -m "feat: add adaptive scaling config with thresholds, scale factor, and ceilings"
```

---

### Task 3: Implement metrics store (SQLite)

**Files:**
- Create: `internal/metrics/store.go`
- Create: `internal/metrics/store_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/metrics/store_test.go`:

```go
package metrics

import (
	"testing"
	"time"
)

func TestStoreRecordAndGetHistory(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer store.Close()

	// Record 3 metrics for the same job
	for i := 0; i < 3; i++ {
		err := store.Record(&MetricsRecord{
			JobName:             "low-cpu-1",
			Profile:             "low-cpu",
			CPUAllocatedNanoCPUs: 1_000_000_000,
			MemAllocatedBytes:   2_147_483_648,
			CPUUsedNanoCPUs:     int64(800_000_000 + i*50_000_000),
			MemPeakBytes:        int64(1_500_000_000 + i*100_000_000),
			DurationSec:         30.0,
		})
		if err != nil {
			t.Fatalf("Record error: %v", err)
		}
		// Small sleep to ensure distinct created_at timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Get history with window of 5 — should return all 3
	history, err := store.GetHistory("low-cpu-1", 5)
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 records, got %d", len(history))
	}

	// Most recent should be first (highest CPU used)
	if history[0].CPUUsedNanoCPUs != 900_000_000 {
		t.Errorf("expected most recent CPU = 900000000, got %d", history[0].CPUUsedNanoCPUs)
	}
}

func TestStoreHistoryWindow(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer store.Close()

	// Record 5 metrics
	for i := 0; i < 5; i++ {
		err := store.Record(&MetricsRecord{
			JobName:             "low-cpu-1",
			Profile:             "low-cpu",
			CPUAllocatedNanoCPUs: 1_000_000_000,
			MemAllocatedBytes:   2_147_483_648,
			CPUUsedNanoCPUs:     int64(500_000_000 + i*100_000_000),
			MemPeakBytes:        1_000_000_000,
			DurationSec:         30.0,
		})
		if err != nil {
			t.Fatalf("Record error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Get history with window of 3 — should return only last 3
	history, err := store.GetHistory("low-cpu-1", 3)
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 records, got %d", len(history))
	}

	// Most recent first: values 900M, 800M, 700M
	if history[0].CPUUsedNanoCPUs != 900_000_000 {
		t.Errorf("expected first CPU = 900000000, got %d", history[0].CPUUsedNanoCPUs)
	}
	if history[2].CPUUsedNanoCPUs != 700_000_000 {
		t.Errorf("expected last CPU = 700000000, got %d", history[2].CPUUsedNanoCPUs)
	}
}

func TestStoreIsolation(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer store.Close()

	// Record metrics for two different jobs
	store.Record(&MetricsRecord{
		JobName:             "low-cpu-1",
		Profile:             "low-cpu",
		CPUAllocatedNanoCPUs: 1_000_000_000,
		MemAllocatedBytes:   2_147_483_648,
		CPUUsedNanoCPUs:     800_000_000,
		MemPeakBytes:        1_500_000_000,
		DurationSec:         30.0,
	})
	store.Record(&MetricsRecord{
		JobName:             "high-cpu",
		Profile:             "high-cpu",
		CPUAllocatedNanoCPUs: 4_000_000_000,
		MemAllocatedBytes:   8_589_934_592,
		CPUUsedNanoCPUs:     3_500_000_000,
		MemPeakBytes:        7_000_000_000,
		DurationSec:         60.0,
	})

	// Query low-cpu-1 — should only get 1 record
	history, err := store.GetHistory("low-cpu-1", 10)
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 record for low-cpu-1, got %d", len(history))
	}
	if history[0].Profile != "low-cpu" {
		t.Errorf("expected profile low-cpu, got %s", history[0].Profile)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/metrics/... -v
```

Expected: Compilation error — package `metrics` does not exist.

- [ ] **Step 3: Implement the metrics store**

Create `internal/metrics/store.go`:

```go
package metrics

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// MetricsRecord holds one observation of a job's resource usage.
type MetricsRecord struct {
	JobName              string
	Profile              string
	CPUAllocatedNanoCPUs int64
	MemAllocatedBytes    int64
	CPUUsedNanoCPUs      int64
	MemPeakBytes         int64
	DurationSec          float64
}

// Store persists job metrics in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite database at the given path.
// Use ":memory:" for in-memory databases (testing).
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening metrics db: %w", err)
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Record inserts a new metrics observation.
func (s *Store) Record(r *MetricsRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO job_metrics (job_name, profile, cpu_allocated_nanocpus, mem_allocated_bytes, cpu_used_nanocpus, mem_peak_bytes, duration_sec)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.JobName, r.Profile, r.CPUAllocatedNanoCPUs, r.MemAllocatedBytes, r.CPUUsedNanoCPUs, r.MemPeakBytes, r.DurationSec,
	)
	if err != nil {
		return fmt.Errorf("inserting metrics: %w", err)
	}
	return nil
}

// GetHistory returns the most recent `limit` records for the given job name,
// ordered by most recent first.
func (s *Store) GetHistory(jobName string, limit int) ([]MetricsRecord, error) {
	rows, err := s.db.Query(`
		SELECT job_name, profile, cpu_allocated_nanocpus, mem_allocated_bytes, cpu_used_nanocpus, mem_peak_bytes, duration_sec
		FROM job_metrics
		WHERE job_name = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		jobName, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying metrics: %w", err)
	}
	defer rows.Close()

	var records []MetricsRecord
	for rows.Next() {
		var r MetricsRecord
		if err := rows.Scan(&r.JobName, &r.Profile, &r.CPUAllocatedNanoCPUs, &r.MemAllocatedBytes, &r.CPUUsedNanoCPUs, &r.MemPeakBytes, &r.DurationSec); err != nil {
			return nil, fmt.Errorf("scanning metrics row: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS job_metrics (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			job_name                TEXT NOT NULL,
			profile                 TEXT NOT NULL,
			cpu_allocated_nanocpus  INTEGER,
			mem_allocated_bytes     INTEGER,
			cpu_used_nanocpus       INTEGER,
			mem_peak_bytes          INTEGER,
			duration_sec            REAL,
			created_at              TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_job_name ON job_metrics(job_name);
	`)
	if err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/metrics/... -v
```

Expected: All 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add internal/metrics/store.go internal/metrics/store_test.go && git commit -m "feat: add SQLite-backed metrics store for job resource usage history"
```

---

### Task 4: Implement the adjuster (pure logic)

**Files:**
- Create: `internal/metrics/adjuster.go`
- Create: `internal/metrics/adjuster_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/metrics/adjuster_test.go`:

```go
package metrics

import (
	"testing"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

func TestAdjustNotEnoughHistory(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      5,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	// Only 3 records when window is 5 — not enough
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 1_000_000_000,
			MemAllocatedBytes:   2_147_483_648,
			CPUUsedNanoCPUs:     900_000_000,
			MemPeakBytes:        1_800_000_000,
		}
	}

	result := adj.Adjust(baseline, history)
	if result.CPUs != "1" {
		t.Errorf("expected CPUs = 1 (baseline), got %s", result.CPUs)
	}
	if result.Memory != "2g" {
		t.Errorf("expected Memory = 2g (baseline), got %s", result.Memory)
	}
	if result.Reason != "insufficient history" {
		t.Errorf("expected reason 'insufficient history', got %q", result.Reason)
	}
}

func TestAdjustScaleUp(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      3,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	// All 3 records show 90% CPU usage, 45% memory usage
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 1_000_000_000,
			MemAllocatedBytes:   2_147_483_648,
			CPUUsedNanoCPUs:     900_000_000,  // 90%
			MemPeakBytes:        966_367_641,  // 45%
		}
	}

	result := adj.Adjust(baseline, history)
	// CPU should scale up: 1 * 1.5 = 1.5
	if result.CPUs != "1.5" {
		t.Errorf("expected CPUs = 1.5, got %s", result.CPUs)
	}
	// Memory should stay at baseline (45% is within thresholds)
	if result.Memory != "2g" {
		t.Errorf("expected Memory = 2g (no change), got %s", result.Memory)
	}
}

func TestAdjustScaleDown(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      3,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "2", Memory: "4g"}
	// All 3 records show 20% CPU and 20% memory usage
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 3_000_000_000, // 3 CPUs (was previously scaled up)
			MemAllocatedBytes:   6_442_450_944,  // 6g (was previously scaled up)
			CPUUsedNanoCPUs:     600_000_000,    // 20%
			MemPeakBytes:        1_288_490_188,  // 20%
		}
	}

	result := adj.Adjust(baseline, history)
	// CPU should scale down: 3 / 1.5 = 2.0
	if result.CPUs != "2" {
		t.Errorf("expected CPUs = 2, got %s", result.CPUs)
	}
	// Memory should scale down: 6g / 1.5 = 4g
	if result.Memory != "4g" {
		t.Errorf("expected Memory = 4g, got %s", result.Memory)
	}
}

func TestAdjustFloor(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      3,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "2", Memory: "4g"}
	// Usage is low, but last allocated = baseline, so scaling down would go below floor
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 2_000_000_000, // = baseline
			MemAllocatedBytes:   4_294_967_296,  // = baseline (4g)
			CPUUsedNanoCPUs:     200_000_000,    // 10%
			MemPeakBytes:        429_496_729,    // 10%
		}
	}

	result := adj.Adjust(baseline, history)
	// Should stay at baseline (floor), not scale down below it
	if result.CPUs != "2" {
		t.Errorf("expected CPUs = 2 (floor), got %s", result.CPUs)
	}
	if result.Memory != "4g" {
		t.Errorf("expected Memory = 4g (floor), got %s", result.Memory)
	}
}

func TestAdjustCeiling(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        2.0,
		HistoryWindow:      3,
		MaxCPUs:            "8",
		MaxMemory:          "16g",
	}

	baseline := &config.Profile{CPUs: "4", Memory: "8g"}
	// High usage with last allocated already at 6 CPUs / 12g
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 6_000_000_000,   // 6 CPUs
			MemAllocatedBytes:   12_884_901_888,   // 12g
			CPUUsedNanoCPUs:     5_400_000_000,    // 90%
			MemPeakBytes:        11_596_411_699,   // 90%
		}
	}

	result := adj.Adjust(baseline, history)
	// CPU: 6 * 2.0 = 12, but ceiling is 8
	if result.CPUs != "8" {
		t.Errorf("expected CPUs = 8 (ceiling), got %s", result.CPUs)
	}
	// Memory: 12g * 2.0 = 24g, but ceiling is 16g
	if result.Memory != "16g" {
		t.Errorf("expected Memory = 16g (ceiling), got %s", result.Memory)
	}
}

func TestAdjustPerProfileCeiling(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        2.0,
		HistoryWindow:      3,
		MaxCPUs:            "16",  // global ceiling
		MaxMemory:          "32g", // global ceiling
	}

	// Profile has tighter per-profile ceilings
	baseline := &config.Profile{CPUs: "4", Memory: "8g", MaxCPUs: "6", MaxMemory: "12g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 4_000_000_000,
			MemAllocatedBytes:   8_589_934_592,
			CPUUsedNanoCPUs:     3_600_000_000,  // 90%
			MemPeakBytes:        7_730_941_132,  // 90%
		}
	}

	result := adj.Adjust(baseline, history)
	// CPU: 4 * 2.0 = 8, but per-profile ceiling is 6
	if result.CPUs != "6" {
		t.Errorf("expected CPUs = 6 (per-profile ceiling), got %s", result.CPUs)
	}
	// Memory: 8g * 2.0 = 16g, but per-profile ceiling is 12g
	if result.Memory != "12g" {
		t.Errorf("expected Memory = 12g (per-profile ceiling), got %s", result.Memory)
	}
}

func TestAdjustWithinThresholds(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      3,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "2", Memory: "4g"}
	// 50% usage — within thresholds
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 2_000_000_000,
			MemAllocatedBytes:   4_294_967_296,
			CPUUsedNanoCPUs:     1_000_000_000,  // 50%
			MemPeakBytes:        2_147_483_648,  // 50%
		}
	}

	result := adj.Adjust(baseline, history)
	if result.CPUs != "2" {
		t.Errorf("expected CPUs = 2 (no change), got %s", result.CPUs)
	}
	if result.Memory != "4g" {
		t.Errorf("expected Memory = 4g (no change), got %s", result.Memory)
	}
	if result.Reason != "within thresholds" {
		t.Errorf("expected reason 'within thresholds', got %q", result.Reason)
	}
}

func TestAdjustCompoundScaleUp(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      3,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	// Last allocated was already 1.5 (previous scale-up), and usage is still high
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{
			CPUAllocatedNanoCPUs: 1_500_000_000, // 1.5 CPUs (previously scaled)
			MemAllocatedBytes:   2_147_483_648,  // 2g (not scaled)
			CPUUsedNanoCPUs:     1_350_000_000,  // 90%
			MemPeakBytes:        1_073_741_824,  // 50%
		}
	}

	result := adj.Adjust(baseline, history)
	// CPU: 1.5 * 1.5 = 2.25
	if result.CPUs != "2.25" {
		t.Errorf("expected CPUs = 2.25, got %s", result.CPUs)
	}
	if result.Memory != "2g" {
		t.Errorf("expected Memory = 2g (no change), got %s", result.Memory)
	}
}

func TestAdjustEmptyHistory(t *testing.T) {
	adj := &Adjuster{
		ScaleUpThreshold:   0.80,
		ScaleDownThreshold: 0.30,
		ScaleFactor:        1.5,
		HistoryWindow:      5,
		MaxCPUs:            "16",
		MaxMemory:          "32g",
	}

	baseline := &config.Profile{CPUs: "1", Memory: "2g"}

	result := adj.Adjust(baseline, nil)
	if result.CPUs != "1" {
		t.Errorf("expected CPUs = 1 (baseline), got %s", result.CPUs)
	}
	if result.Memory != "2g" {
		t.Errorf("expected Memory = 2g (baseline), got %s", result.Memory)
	}
	if result.Reason != "insufficient history" {
		t.Errorf("expected reason 'insufficient history', got %q", result.Reason)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/metrics/... -v -run TestAdjust
```

Expected: Compilation error — `Adjuster` type does not exist.

- [ ] **Step 3: Implement the adjuster**

Create `internal/metrics/adjuster.go`:

```go
package metrics

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

// Adjuster computes adjusted CPU/memory values based on historical usage.
type Adjuster struct {
	ScaleUpThreshold   float64
	ScaleDownThreshold float64
	ScaleFactor        float64
	HistoryWindow      int
	MaxCPUs            string
	MaxMemory          string
}

// AdjustedResources holds the adjusted CPU/memory values for provisioning.
type AdjustedResources struct {
	CPUs   string
	Memory string
	Reason string
}

// Adjust returns adjusted resource values based on the baseline profile and historical metrics.
// CPU and memory are scaled independently. The baseline acts as a floor, and per-profile or
// global ceilings cap the maximum.
func (a *Adjuster) Adjust(baseline *config.Profile, history []MetricsRecord) *AdjustedResources {
	if len(history) < a.HistoryWindow {
		return &AdjustedResources{
			CPUs:   baseline.CPUs,
			Memory: baseline.Memory,
			Reason: "insufficient history",
		}
	}

	// Compute average utilization across the window
	var cpuUtilSum, memUtilSum float64
	for _, r := range history {
		if r.CPUAllocatedNanoCPUs > 0 {
			cpuUtilSum += float64(r.CPUUsedNanoCPUs) / float64(r.CPUAllocatedNanoCPUs)
		}
		if r.MemAllocatedBytes > 0 {
			memUtilSum += float64(r.MemPeakBytes) / float64(r.MemAllocatedBytes)
		}
	}
	cpuUtil := cpuUtilSum / float64(len(history))
	memUtil := memUtilSum / float64(len(history))

	// Get last allocated values (most recent record) as base for compounding
	lastCPU := float64(history[0].CPUAllocatedNanoCPUs)
	lastMem := float64(history[0].MemAllocatedBytes)

	// Parse baseline as floor
	baselineCPUNano := parseCPUToNano(baseline.CPUs)
	baselineMemBytes := parseMemToBytes(baseline.Memory)

	// If last allocated is 0 (edge case), fall back to baseline
	if lastCPU == 0 {
		lastCPU = baselineCPUNano
	}
	if lastMem == 0 {
		lastMem = baselineMemBytes
	}

	// Parse ceilings
	maxCPUNano := parseCPUToNano(a.MaxCPUs)
	maxMemBytes := parseMemToBytes(a.MaxMemory)

	// Per-profile ceilings override global if set
	if baseline.MaxCPUs != "" {
		profileMaxCPU := parseCPUToNano(baseline.MaxCPUs)
		if profileMaxCPU < maxCPUNano {
			maxCPUNano = profileMaxCPU
		}
	}
	if baseline.MaxMemory != "" {
		profileMaxMem := parseMemToBytes(baseline.MaxMemory)
		if profileMaxMem < maxMemBytes {
			maxMemBytes = profileMaxMem
		}
	}

	// Apply scaling
	newCPU := lastCPU
	newMem := lastMem
	var reasons []string

	if cpuUtil > a.ScaleUpThreshold {
		newCPU = lastCPU * a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("CPU scaled up: avg util %.0f%%", cpuUtil*100))
	} else if cpuUtil < a.ScaleDownThreshold {
		newCPU = lastCPU / a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("CPU scaled down: avg util %.0f%%", cpuUtil*100))
	}

	if memUtil > a.ScaleUpThreshold {
		newMem = lastMem * a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("memory scaled up: avg util %.0f%%", memUtil*100))
	} else if memUtil < a.ScaleDownThreshold {
		newMem = lastMem / a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("memory scaled down: avg util %.0f%%", memUtil*100))
	}

	// Apply floor (baseline)
	if newCPU < baselineCPUNano {
		newCPU = baselineCPUNano
	}
	if newMem < baselineMemBytes {
		newMem = baselineMemBytes
	}

	// Apply ceiling
	if newCPU > maxCPUNano {
		newCPU = maxCPUNano
	}
	if newMem > maxMemBytes {
		newMem = maxMemBytes
	}

	reason := "within thresholds"
	if len(reasons) > 0 {
		reason = strings.Join(reasons, "; ")
	}

	return &AdjustedResources{
		CPUs:   formatCPU(newCPU),
		Memory: formatMemory(newMem),
		Reason: reason,
	}
}

// parseCPUToNano converts a CPU string like "4" or "1.5" to nanocpus.
func parseCPUToNano(cpus string) float64 {
	f, err := strconv.ParseFloat(cpus, 64)
	if err != nil {
		return 0
	}
	return f * 1e9
}

// parseMemToBytes converts a memory string like "8g" or "512m" to bytes.
func parseMemToBytes(mem string) float64 {
	mem = strings.TrimSpace(mem)
	if len(mem) == 0 {
		return 0
	}
	suffix := strings.ToLower(mem[len(mem)-1:])
	numStr := mem[:len(mem)-1]
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		// Try parsing entire string as bytes
		b, err2 := strconv.ParseFloat(mem, 64)
		if err2 != nil {
			return 0
		}
		return b
	}
	switch suffix {
	case "g":
		return num * 1024 * 1024 * 1024
	case "m":
		return num * 1024 * 1024
	case "k":
		return num * 1024
	default:
		b, _ := strconv.ParseFloat(mem, 64)
		return b
	}
}

// formatCPU converts nanocpus back to a human-readable CPU string.
// Produces integers when possible (e.g., "4"), otherwise up to 2 decimal places (e.g., "1.5", "2.25").
func formatCPU(nanoCPUs float64) string {
	cpus := nanoCPUs / 1e9
	if cpus == math.Trunc(cpus) {
		return strconv.Itoa(int(cpus))
	}
	s := strconv.FormatFloat(cpus, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// formatMemory converts bytes back to a human-readable memory string using the largest clean unit.
func formatMemory(bytes float64) string {
	gb := bytes / (1024 * 1024 * 1024)
	if gb == math.Trunc(gb) && gb >= 1 {
		return fmt.Sprintf("%dg", int(gb))
	}
	mb := bytes / (1024 * 1024)
	if mb == math.Trunc(mb) && mb >= 1 {
		return fmt.Sprintf("%dm", int(mb))
	}
	return strconv.FormatInt(int64(bytes), 10)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/metrics/... -v -run TestAdjust
```

Expected: All 9 adjuster tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add internal/metrics/adjuster.go internal/metrics/adjuster_test.go && git commit -m "feat: add adjuster with threshold-based bidirectional scaling and ceiling/floor logic"
```

---

### Task 5: Implement the cgroup collector

**Files:**
- Create: `internal/metrics/collector.go`
- Create: `internal/metrics/collector_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/metrics/collector_test.go`:

```go
package metrics

import (
	"testing"
	"time"
)

func TestParseCgroupV2CPU(t *testing.T) {
	// cpu.stat content with usage_usec line
	content := `usage_usec 15000000
user_usec 10000000
system_usec 5000000
nr_periods 0
nr_throttled 0
throttled_usec 0`

	usageUsec, err := parseCPUStatUsageUsec(content)
	if err != nil {
		t.Fatalf("parseCPUStatUsageUsec error: %v", err)
	}
	if usageUsec != 15_000_000 {
		t.Errorf("expected 15000000, got %d", usageUsec)
	}

	// Convert to nanocpus: (15_000_000 usec * 1000) / 30 sec = 500_000_000 nanocpus
	duration := 30 * time.Second
	nanoCPUs := usageUsecToNanoCPUs(usageUsec, duration)
	if nanoCPUs != 500_000_000 {
		t.Errorf("expected 500000000, got %d", nanoCPUs)
	}
}

func TestParseCgroupV2Memory(t *testing.T) {
	// memory.peak content — just a single number
	content := "1073741824\n"
	bytes, err := parseMemoryPeak(content)
	if err != nil {
		t.Fatalf("parseMemoryPeak error: %v", err)
	}
	if bytes != 1_073_741_824 {
		t.Errorf("expected 1073741824, got %d", bytes)
	}
}

func TestParseCgroupV1CPU(t *testing.T) {
	// cpuacct.usage contains total nanoseconds
	content := "15000000000\n"
	nanos, err := parseCPUAcctUsage(content)
	if err != nil {
		t.Fatalf("parseCPUAcctUsage error: %v", err)
	}

	// Convert nanoseconds to nanocpus: nanos / duration_sec
	duration := 30 * time.Second
	nanoCPUs := cpuAcctNanosToNanoCPUs(nanos, duration)
	// 15_000_000_000 ns / 30 s = 500_000_000 nanocpus
	if nanoCPUs != 500_000_000 {
		t.Errorf("expected 500000000, got %d", nanoCPUs)
	}
}

func TestParseCgroupV1Memory(t *testing.T) {
	// memory.max_usage_in_bytes — single number
	content := "2147483648\n"
	bytes, err := parseMemoryPeak(content)
	if err != nil {
		t.Fatalf("parseMemoryPeak error: %v", err)
	}
	if bytes != 2_147_483_648 {
		t.Errorf("expected 2147483648, got %d", bytes)
	}
}

func TestParseCPUStatMissingUsageLine(t *testing.T) {
	content := `user_usec 10000000
system_usec 5000000`

	_, err := parseCPUStatUsageUsec(content)
	if err == nil {
		t.Error("expected error for missing usage_usec line")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/metrics/... -v -run "TestParseCgroup|TestParseCPU"
```

Expected: Compilation error — parse functions do not exist.

- [ ] **Step 3: Implement the collector**

Create `internal/metrics/collector.go`:

```go
package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Collector reads cgroup metrics from a running container via Docker exec.
type Collector interface {
	Collect(ctx context.Context, containerID string, duration time.Duration) (*JobMetrics, error)
}

// JobMetrics holds the raw metrics collected from a container's cgroups.
type JobMetrics struct {
	CPUUsedNanoCPUs int64
	MemPeakBytes    int64
}

// DockerCollector implements Collector using the Docker exec API.
type DockerCollector struct {
	docker client.APIClient
}

// NewDockerCollector creates a new DockerCollector.
func NewDockerCollector(docker client.APIClient) *DockerCollector {
	return &DockerCollector{docker: docker}
}

// Collect reads cgroup metrics from the container. Tries cgroup v2 first, then v1.
func (c *DockerCollector) Collect(ctx context.Context, containerID string, duration time.Duration) (*JobMetrics, error) {
	metrics := &JobMetrics{}

	// Try cgroup v2 CPU
	cpuContent, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/cpu.stat")
	if err == nil {
		usageUsec, parseErr := parseCPUStatUsageUsec(cpuContent)
		if parseErr == nil {
			metrics.CPUUsedNanoCPUs = usageUsecToNanoCPUs(usageUsec, duration)
		}
	} else {
		// Fall back to cgroup v1
		v1Content, v1Err := c.execRead(ctx, containerID, "/sys/fs/cgroup/cpu/cpuacct.usage")
		if v1Err == nil {
			nanos, parseErr := parseCPUAcctUsage(v1Content)
			if parseErr == nil {
				metrics.CPUUsedNanoCPUs = cpuAcctNanosToNanoCPUs(nanos, duration)
			}
		}
	}

	// Try cgroup v2 memory
	memContent, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/memory.peak")
	if err == nil {
		peakBytes, parseErr := parseMemoryPeak(memContent)
		if parseErr == nil {
			metrics.MemPeakBytes = peakBytes
		}
	} else {
		// Fall back to cgroup v1
		v1Content, v1Err := c.execRead(ctx, containerID, "/sys/fs/cgroup/memory/memory.max_usage_in_bytes")
		if v1Err == nil {
			peakBytes, parseErr := parseMemoryPeak(v1Content)
			if parseErr == nil {
				metrics.MemPeakBytes = peakBytes
			}
		}
	}

	if metrics.CPUUsedNanoCPUs == 0 && metrics.MemPeakBytes == 0 {
		return nil, fmt.Errorf("no cgroup metrics found in container %s", containerID[:12])
	}

	return metrics, nil
}

// execRead runs `cat <path>` inside the container and returns stdout.
func (c *DockerCollector) execRead(ctx context.Context, containerID, path string) (string, error) {
	execCfg := container.ExecOptions{
		Cmd:          []string{"cat", path},
		AttachStdout: true,
		AttachStderr: true,
	}

	execID, err := c.docker.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}

	resp, err := c.docker.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, resp.Reader)

	// Check exit code
	inspect, err := c.docker.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("cat %s exited with code %d", path, inspect.ExitCode)
	}

	return buf.String(), nil
}

// parseCPUStatUsageUsec extracts usage_usec from cgroup v2 cpu.stat content.
func parseCPUStatUsageUsec(content string) (int64, error) {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "usage_usec" {
			return strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec not found in cpu.stat")
}

// usageUsecToNanoCPUs converts microseconds of CPU time to average nanocpus over duration.
func usageUsecToNanoCPUs(usageUsec int64, duration time.Duration) int64 {
	if duration.Seconds() == 0 {
		return 0
	}
	return int64(float64(usageUsec) * 1000 / duration.Seconds())
}

// parseCPUAcctUsage parses cgroup v1 cpuacct.usage (total nanoseconds).
func parseCPUAcctUsage(content string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(content), 10, 64)
}

// cpuAcctNanosToNanoCPUs converts total nanoseconds to average nanocpus over duration.
func cpuAcctNanosToNanoCPUs(totalNanos int64, duration time.Duration) int64 {
	if duration.Seconds() == 0 {
		return 0
	}
	return int64(float64(totalNanos) / duration.Seconds())
}

// parseMemoryPeak parses a single integer from memory.peak or memory.max_usage_in_bytes.
func parseMemoryPeak(content string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(content), 10, 64)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/metrics/... -v -run "TestParseCgroup|TestParseCPU"
```

Expected: All 5 parse/convert tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add internal/metrics/collector.go internal/metrics/collector_test.go && git commit -m "feat: add cgroup metrics collector with v2/v1 fallback and Docker exec"
```

---

### Task 6: Wire adaptive scaling into the scaler

**Files:**
- Modify: `internal/state/state.go:18-29` (RunnerInfo struct — add AllocatedCPUs, AllocatedMemory)
- Modify: `internal/scaler/scaler.go:1-13` (imports)
- Modify: `internal/scaler/scaler.go:41-56` (Scaler struct)
- Modify: `internal/scaler/scaler.go:59-82` (New function)
- Modify: `internal/scaler/scaler.go:179-201` (handleJobAssigned)
- Modify: `internal/scaler/scaler.go:204-238` (provisionRunner)
- Modify: `internal/scaler/scaler.go:261-298` (handleJobCompleted)

- [ ] **Step 1: Add allocated resource fields to RunnerInfo**

In `internal/state/state.go`, add two fields to the `RunnerInfo` struct after `JobName` (line 25):

```go
AllocatedCPUs   string       // Effective CPUs provisioned (may differ from profile baseline)
AllocatedMemory string       // Effective memory provisioned (may differ from profile baseline)
```

- [ ] **Step 2: Add metrics fields to Scaler struct**

In `internal/scaler/scaler.go`, add to imports:

```go
"time"

"github.com/reidg44/gh-runners-proxy-assignment/internal/metrics"
```

Add three new fields to the `Scaler` struct (after `proxyURL string`, line 49):

```go
metricsCollector metrics.Collector
metricsStore     *metrics.Store
adjuster         *metrics.Adjuster
```

- [ ] **Step 3: Update the New constructor**

Update the `New` function signature to accept optional metrics components. Add three new parameters after `proxyURL string`:

```go
func New(
	sessionClient SessionClient,
	jitGenerator JITConfigGenerator,
	provisioner RunnerProvisioner,
	classifier *classifier.Classifier,
	store *state.Store,
	cfg *config.Config,
	scaleSetID int,
	proxyURL string,
	metricsCollector metrics.Collector,
	metricsStore *metrics.Store,
	adjuster *metrics.Adjuster,
	logger *slog.Logger,
) *Scaler {
	return &Scaler{
		sessionClient:    sessionClient,
		jitGenerator:     jitGenerator,
		provisioner:      provisioner,
		classifier:       classifier,
		store:            store,
		cfg:              cfg,
		scaleSetID:       scaleSetID,
		proxyURL:         proxyURL,
		metricsCollector: metricsCollector,
		metricsStore:     metricsStore,
		adjuster:         adjuster,
		logger:           logger,
		pendingJobs:      make(map[string]*pendingJob),
	}
}
```

- [ ] **Step 4: Update provisionRunner to apply adaptive adjustments**

Replace the `provisionRunner` method (lines 204-238) with:

```go
func (s *Scaler) provisionRunner(ctx context.Context, jobDisplayName, jobID, profileName string, profile *config.Profile) error {
	// Generate JIT runner config
	runnerName := fmt.Sprintf("runner-%s-%s", profileName, jobID)
	jitCfg, err := s.jitGenerator.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{
		Name:       runnerName,
		WorkFolder: s.cfg.Runner.WorkFolder,
	}, s.scaleSetID)
	if err != nil {
		return fmt.Errorf("generating JIT config: %w", err)
	}

	// Apply adaptive adjustments if available
	effectiveProfile := profile
	if s.adjuster != nil && s.metricsStore != nil {
		history, err := s.metricsStore.GetHistory(jobDisplayName, s.adjuster.HistoryWindow)
		if err != nil {
			s.logger.Warn("failed to get metrics history, using baseline",
				"job_display_name", jobDisplayName,
				"error", err,
			)
		} else {
			adjusted := s.adjuster.Adjust(profile, history)
			s.logger.Info("adaptive adjustment",
				"job_display_name", jobDisplayName,
				"baseline_cpus", profile.CPUs,
				"baseline_memory", profile.Memory,
				"adjusted_cpus", adjusted.CPUs,
				"adjusted_memory", adjusted.Memory,
				"reason", adjusted.Reason,
			)
			effectiveProfile = &config.Profile{
				CPUs:   adjusted.CPUs,
				Memory: adjusted.Memory,
			}
		}
	}

	// Start the runner container
	containerID, containerIP, err := s.provisioner.StartRunner(ctx, runnerName, effectiveProfile, jitCfg.EncodedJITConfig, s.proxyURL)
	if err != nil {
		return fmt.Errorf("starting runner container: %w", err)
	}

	s.store.AddRunner(&state.RunnerInfo{
		RunnerName:      runnerName,
		ContainerID:     containerID,
		ContainerIP:     containerIP,
		Profile:         profileName,
		JobID:           jobID,
		JobName:         jobDisplayName,
		AllocatedCPUs:   effectiveProfile.CPUs,
		AllocatedMemory: effectiveProfile.Memory,
	})

	s.logger.Info("runner provisioned",
		"runner_name", runnerName,
		"container_id", truncateID(containerID),
		"profile", profileName,
		"cpus", effectiveProfile.CPUs,
		"memory", effectiveProfile.Memory,
		"job_display_name", jobDisplayName,
	)

	return nil
}
```

- [ ] **Step 5: Update handleJobCompleted to collect metrics**

Replace the `handleJobCompleted` method (lines 261-298) with:

```go
func (s *Scaler) handleJobCompleted(ctx context.Context, job *scaleset.JobCompleted) {
	s.logger.Info("job completed",
		"runner_name", job.RunnerName,
		"job_display_name", job.JobDisplayName,
		"result", job.Result,
	)

	// Remove from pending in case it was canceled before starting
	s.mu.Lock()
	delete(s.pendingJobs, job.JobID)
	s.mu.Unlock()

	if job.RunnerName == "" {
		s.logger.Warn("completed job with empty runner name",
			"job_display_name", job.JobDisplayName,
			"result", job.Result,
		)
		return
	}

	s.store.MarkCompleted(job.RunnerName)

	runner, ok := s.store.GetByName(job.RunnerName)
	if !ok {
		s.logger.Warn("completed job for unknown runner", "runner_name", job.RunnerName)
		return
	}

	// Collect cgroup metrics before stopping the container
	if s.metricsCollector != nil && s.metricsStore != nil {
		duration := time.Since(runner.StartedAt)
		if duration <= 0 {
			duration = time.Second // safety minimum
		}

		jobMetrics, err := s.metricsCollector.Collect(ctx, runner.ContainerID, duration)
		if err != nil {
			s.logger.Warn("failed to collect metrics",
				"runner_name", job.RunnerName,
				"error", err,
			)
		} else {
			// Use actual allocated values (which may have been adjusted by the adjuster)
			allocCPU := int64(metrics.ParseCPUToNano(runner.AllocatedCPUs))
			allocMem := int64(metrics.ParseMemToBytes(runner.AllocatedMemory))

			if err := s.metricsStore.Record(&metrics.MetricsRecord{
				JobName:              runner.JobName,
				Profile:              runner.Profile,
				CPUAllocatedNanoCPUs: allocCPU,
				MemAllocatedBytes:    allocMem,
				CPUUsedNanoCPUs:      jobMetrics.CPUUsedNanoCPUs,
				MemPeakBytes:         jobMetrics.MemPeakBytes,
				DurationSec:          duration.Seconds(),
			}); err != nil {
				s.logger.Warn("failed to record metrics",
					"runner_name", job.RunnerName,
					"error", err,
				)
			} else {
				s.logger.Info("metrics recorded",
					"runner_name", job.RunnerName,
					"cpu_used", jobMetrics.CPUUsedNanoCPUs,
					"mem_peak", jobMetrics.MemPeakBytes,
					"duration", duration.Seconds(),
				)
			}
		}
	}

	if err := s.provisioner.StopRunner(ctx, runner.ContainerID); err != nil {
		s.logger.Error("failed to stop runner container",
			"runner_name", job.RunnerName,
			"container_id", runner.ContainerID,
			"error", err,
		)
	}

	s.store.Remove(job.RunnerName)
}
```

- [ ] **Step 6: Export parse helpers from adjuster for the scaler to use**

In `internal/metrics/adjuster.go`, rename `parseCPUToNano` and `parseMemToBytes` to exported names:

```go
// ParseCPUToNano converts a CPU string like "4" or "1.5" to nanocpus.
func ParseCPUToNano(cpus string) float64 {
```

```go
// ParseMemToBytes converts a memory string like "8g" or "512m" to bytes.
func ParseMemToBytes(mem string) float64 {
```

Update all internal references in `adjuster.go` from `parseCPUToNano` to `ParseCPUToNano` and `parseMemToBytes` to `ParseMemToBytes`.

- [ ] **Step 7: Build to verify compilation**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go build ./internal/...
```

Expected: Compilation errors in `cmd/all/main.go` and `cmd/listener/main.go` because `scalerpkg.New()` now expects additional parameters. This is expected and will be fixed in Task 7.

- [ ] **Step 8: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add internal/state/state.go internal/scaler/scaler.go internal/metrics/adjuster.go && git commit -m "feat: wire adaptive metrics collection and adjustment into scaler lifecycle"
```

---

### Task 7: Update entry points to initialize metrics components

**Files:**
- Modify: `cmd/all/main.go:1-22` (imports), `cmd/all/main.go:159-161` (scaler init)
- Modify: `cmd/listener/main.go:1-19` (imports), `cmd/listener/main.go:124-134` (scaler init)

- [ ] **Step 1: Update cmd/all/main.go**

Add to imports:

```go
"github.com/reidg44/gh-runners-proxy-assignment/internal/metrics"
```

In the `run` function, after the provisioner is created and proxyURL is computed (after line 157), add metrics initialization before the classifier/scaler setup:

```go
	// Initialize adaptive metrics components (if enabled)
	var metricsCollector metrics.Collector
	var metricsStore *metrics.Store
	var adjuster *metrics.Adjuster

	if cfg.Adaptive.Enabled {
		logger.Info("adaptive scaling enabled", "db_path", cfg.Adaptive.DBPath)
		var err error
		metricsStore, err = metrics.NewStore(cfg.Adaptive.DBPath)
		if err != nil {
			return fmt.Errorf("opening metrics store: %w", err)
		}
		defer metricsStore.Close()

		dockerCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return fmt.Errorf("creating Docker client for metrics: %w", err)
		}
		metricsCollector = metrics.NewDockerCollector(dockerCli)

		adjuster = &metrics.Adjuster{
			ScaleUpThreshold:   cfg.Adaptive.ScaleUpThreshold,
			ScaleDownThreshold: cfg.Adaptive.ScaleDownThreshold,
			ScaleFactor:        cfg.Adaptive.ScaleFactor,
			HistoryWindow:      cfg.Adaptive.HistoryWindow,
			MaxCPUs:            cfg.Adaptive.MaxCPUs,
			MaxMemory:          cfg.Adaptive.MaxMemory,
		}
	}
```

Also add the Docker client import alias at the top:

```go
dockerclient "github.com/docker/docker/client"
```

Update the `scalerpkg.New()` call (line 161) to pass the new parameters:

```go
	s := scalerpkg.New(sessionClient, client, prov, cls, store, cfg, scaleSet.ID, proxyURL, metricsCollector, metricsStore, adjuster, logger)
```

- [ ] **Step 2: Update cmd/listener/main.go**

Apply the same changes: add imports for `metrics` and `dockerclient`, add metrics initialization after provisioner creation (after line 132), update the `scalerpkg.New()` call (line 134):

```go
	// Initialize adaptive metrics components (if enabled)
	var metricsCollector metrics.Collector
	var metricsStore *metrics.Store
	var adjuster *metrics.Adjuster

	if cfg.Adaptive.Enabled {
		logger.Info("adaptive scaling enabled", "db_path", cfg.Adaptive.DBPath)
		var err error
		metricsStore, err = metrics.NewStore(cfg.Adaptive.DBPath)
		if err != nil {
			return fmt.Errorf("opening metrics store: %w", err)
		}
		defer metricsStore.Close()

		dockerCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return fmt.Errorf("creating Docker client for metrics: %w", err)
		}
		metricsCollector = metrics.NewDockerCollector(dockerCli)

		adjuster = &metrics.Adjuster{
			ScaleUpThreshold:   cfg.Adaptive.ScaleUpThreshold,
			ScaleDownThreshold: cfg.Adaptive.ScaleDownThreshold,
			ScaleFactor:        cfg.Adaptive.ScaleFactor,
			HistoryWindow:      cfg.Adaptive.HistoryWindow,
			MaxCPUs:            cfg.Adaptive.MaxCPUs,
			MaxMemory:          cfg.Adaptive.MaxMemory,
		}
	}

	s := scalerpkg.New(sessionClient, client, prov, cls, store, cfg, scaleSet.ID, proxyURL, metricsCollector, metricsStore, adjuster, logger)
```

- [ ] **Step 3: Build to verify compilation**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go build ./cmd/all && go build ./cmd/listener
```

Expected: Both compile successfully.

- [ ] **Step 4: Run all tests**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/... -v
```

Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add cmd/all/main.go cmd/listener/main.go && git commit -m "feat: initialize adaptive metrics components in entry points"
```

---

### Task 8: Run full test suite and lint

**Files:** None (verification only)

- [ ] **Step 1: Run all tests**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go test ./internal/... -v
```

Expected: All tests PASS.

- [ ] **Step 2: Run go vet**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go vet ./internal/... && go vet ./cmd/...
```

Expected: No issues.

- [ ] **Step 3: Build all binaries**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && go build -o bin/gh-proxy ./cmd/all && go build -o bin/listener ./cmd/listener
```

Expected: Both build successfully.

- [ ] **Step 4: Run pre-commit checks**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && prek run --all-files
```

Expected: All checks pass.

---

### Task 9: Update documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `config.yaml` (already done in Task 2, verify)

- [ ] **Step 1: Update CLAUDE.md**

Add a new section under "### Internal Packages" for the metrics package:

```markdown
- **`internal/metrics/`** — Adaptive resource scaling. Three components: `Store` (SQLite-backed history of per-job CPU/memory usage), `DockerCollector` (reads cgroup v2/v1 metrics from containers via `docker exec`), and `Adjuster` (pure function computing adjusted CPU/memory from baseline profile + historical usage). Thresholds, scale factor, history window, and ceilings are configured in the `adaptive` section of `config.yaml`.
```

Add under "### Key Design Decisions":

```markdown
- **Adaptive scaling at provisioning time** — The adjuster overrides resource values when creating containers, but `config.yaml` profiles remain the source of truth for baselines and floors. This keeps the config clean while allowing runtime optimization. SQLite stores usage history across restarts. The collector reads cgroup files (not Docker stats API) for Kubernetes portability.
```

Add under "## Test Infrastructure":

```markdown
- **`test-adaptive-scaling.yaml`** — Two-phase test: phase 1 stresses CPU/memory under `low-cpu` baseline, phase 2 verifies the adaptive system provisioned higher limits. Requires `adaptive.history_window: 1` in config. Summary job reports baseline vs adjusted limits.
```

- [ ] **Step 2: Commit**

```bash
cd /Users/reidgeyer/projects/gh-runners-proxy-assignment && git add CLAUDE.md && git commit -m "docs: add adaptive scaling documentation to CLAUDE.md"
```

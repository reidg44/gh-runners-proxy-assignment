# Adaptive Resource Scaling Design

## Overview

Add adaptive resource scaling to the GitHub Actions runner proxy system. The system monitors actual CPU and memory usage of running jobs via cgroup metrics, stores history in SQLite, and automatically adjusts resource allocations for future runs when usage crosses configurable thresholds.

## Requirements

- **Collection:** Read cgroup v2 (with v1 fallback) metrics from containers at job completion, before container stop
- **Storage:** SQLite database for usage history, queryable by job name
- **Adjustment trigger:** Threshold-based — scale up if average usage exceeds upper threshold, scale down if below lower threshold
- **Direction:** Bidirectional scaling (up and down)
- **Application:** Override resource values at provisioning time; `config.yaml` profiles remain as baselines
- **Portability:** cgroup-based collection works in both Docker and Kubernetes environments
- **Configuration:** Thresholds, scale factor, history window, and ceilings are all user-configurable in `config.yaml`
- **Feature flag:** `adaptive.enabled` controls the entire feature; when disabled, the system behaves exactly as before

## Data Model

### SQLite Schema (`metrics.db`)

```sql
CREATE TABLE job_metrics (
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

CREATE INDEX idx_job_name ON job_metrics(job_name);
```

- Keyed by `job_name` (JobDisplayName) since the classifier matches on this value
- Stores both allocated and used values for utilization ratio computation
- `duration_sec` is wall-clock time (`CompletedAt - StartedAt`), used to derive average CPU from cumulative `usage_usec`

## Metrics Collection

### Package: `internal/metrics/collector.go`

Reads cgroup files from inside the container via Docker exec API (`ContainerExecCreate` / `ContainerExecAttach`) just before the container is stopped.

**Files read:**

| Metric | cgroup v2 path | cgroup v1 fallback |
|--------|---------------|-------------------|
| Peak memory | `/sys/fs/cgroup/memory.peak` | `/sys/fs/cgroup/memory/memory.max_usage_in_bytes` |
| CPU time | `/sys/fs/cgroup/cpu.stat` (`usage_usec` line) | `/sys/fs/cgroup/cpu/cpuacct.usage` |

**CPU calculation:** `cpu_used_nanocpus = (usage_usec * 1000) / duration_sec`

**Interface:**

```go
type Collector interface {
    Collect(ctx context.Context, containerID string, duration time.Duration) (*JobMetrics, error)
}

type JobMetrics struct {
    CPUUsedNanoCPUs int64
    MemPeakBytes    int64
}
```

**Fallback:** If neither cgroup v2 nor v1 files are found, log a warning and skip — no metrics recorded for that run. The system continues to function without adaptive adjustments.

**Why Docker exec:** Container cgroup paths on the host vary by Docker version and cgroup driver. Reading from inside the container uses consistent `/sys/fs/cgroup/` paths — the same paths the test workflow already validates. This pattern maps directly to `kubectl exec` for Kubernetes migration.

## Adjustment Logic

### Package: `internal/metrics/adjuster.go`

A pure function that takes a baseline profile and historical metrics, then returns adjusted CPU/memory values.

**Interface:**

```go
type Adjuster struct {
    ScaleUpThreshold   float64
    ScaleDownThreshold float64
    ScaleFactor        float64
    HistoryWindow      int
}

type AdjustedResources struct {
    CPUs   string
    Memory string
    Reason string
}

func (a *Adjuster) Adjust(baseline config.Profile, history []JobMetrics) *AdjustedResources
```

**Algorithm:**

1. Query last `HistoryWindow` metrics for this `job_name` from SQLite
2. If fewer than `HistoryWindow` records exist, return baseline (not enough data)
3. Compute average CPU utilization: `avg(cpu_used / cpu_allocated)` across the window
4. Compute average memory utilization: `avg(mem_peak / mem_allocated)` across the window
5. **Scale up:** If avg utilization > `ScaleUpThreshold`, multiply the *last allocated value* by `ScaleFactor`
6. **Scale down:** If avg utilization < `ScaleDownThreshold`, divide the last allocated value by `ScaleFactor`
7. **Floor:** Never go below the baseline profile values from `config.yaml`
8. **Ceiling:** Never exceed `max_cpus`/`max_memory` (per-profile override or global default)
9. CPU and memory scale independently of each other
10. If no adjustment needed, return baseline values with `Reason: "within thresholds"`

**Compounding:** The adjuster reads the last allocated value from the most recent record in `history` (`history[0].CPUAllocatedNanoCPUs` / `history[0].MemAllocatedBytes`) and applies the scale factor to that, not to the baseline. This means repeated high usage compounds — each scale-up builds on the previous allocation until the ceiling is reached. Repeated low usage compounds downward until the floor (baseline) is reached. If history exists but the most recent record has no allocated value (edge case), fall back to baseline.

**Example:**
- `low-cpu` baseline: 1 CPU, 2g memory
- Last 5 runs averaged 88% CPU, 45% memory
- Scale up CPU: `1 * 1.5 = 1.5` CPUs; memory stays at 2g
- If next 5 runs at 1.5 CPUs still average 85%: `1.5 * 1.5 = 2.25` CPUs
- Ceiling of 8 CPUs prevents unbounded growth

## Configuration

### New `config.yaml` section

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

### Per-profile ceiling overrides (optional)

```yaml
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
    # Uses global ceiling defaults
```

Per-profile `max_cpus`/`max_memory` override global values. Global values act as the default ceiling.

## Integration Points

### Scaler: modified `provisionRunner()` flow

```
1. profile := classifier.Classify(jobDisplayName)                                  // existing
2. baseline := config.Profiles[profile]                                            // existing
3. adjusted := adjuster.Adjust(baseline, metricsStore.GetHistory(jobDisplayName))  // NEW
4. log adjusted.Reason                                                             // NEW
5. provisioner.StartRunner(name, adjusted, jitConfig, proxyURL)                    // passes adjusted values
```

### Scaler: modified `handleJobCompleted()` flow

```
1. runner := store.GetByName(runnerName)                                           // existing
2. duration := time.Since(runner.StartedAt)                                        // NEW
3. metrics := collector.Collect(ctx, runner.ContainerID, duration)                 // NEW
4. metricsStore.Record(runner.JobName, runner.Profile, allocated, metrics, duration) // NEW
5. provisioner.StopRunner(runner.ContainerID)                                      // existing
6. store.Remove(runnerName)                                                        // existing
```

### Entry points: `cmd/all/main.go` and `cmd/listener/main.go`

Initialize metrics components (SQLite store, collector, adjuster) and pass to scaler. Skip initialization entirely when `adaptive.enabled` is false.

## Package Structure

### New files

```
internal/metrics/
├── collector.go       # Reads cgroup files via Docker exec
├── collector_test.go  # Mock Docker API, verify cgroup output parsing
├── store.go           # SQLite operations (Record, GetHistory)
├── store_test.go      # In-memory SQLite, verify queries and window behavior
├── adjuster.go        # Pure adjustment logic
└── adjuster_test.go   # Table-driven tests with various usage scenarios
```

### Modified files

```
internal/config/config.go   # Add Adaptive struct, per-profile ceiling fields, validation
internal/scaler/scaler.go   # Wire collector, store, adjuster into job lifecycle
cmd/all/main.go             # Initialize metrics components, pass to scaler
cmd/listener/main.go                              # Same initialization for standalone mode
.github/workflows/test-adaptive-scaling.yaml      # End-to-end test workflow
```

## Testing Strategy

### Adjuster tests (unit, pure logic)

Table-driven tests covering:
- Not enough history → returns baseline
- Usage above threshold → scales up by scale factor
- Usage below threshold → scales down by scale factor
- Multiple consecutive scale-ups → compounds correctly
- Hits ceiling → capped at max
- Hits floor (baseline) → never goes below config values
- CPU and memory scale independently

### Store tests (unit, in-memory SQLite)

- Record and retrieve metrics by job name
- History window returns only last N records ordered by recency
- Multiple job names don't interfere with each other

### Collector tests (unit, mocked Docker)

- Parse cgroup v2 output correctly (`memory.peak`, `cpu.stat`)
- Parse cgroup v1 fallback correctly
- Handle missing files gracefully (return error, no panic)

### Scaler integration test

- Verify full flow: job completes → metrics collected → next provision uses adjusted values
- Mock collector and SQLite store at interface boundaries

## End-to-End Test Workflow

### `.github/workflows/test-adaptive-scaling.yaml`

A two-phase workflow that validates the full adaptive scaling loop. **Prerequisite:** the system must be running with `adaptive.enabled: true` and `adaptive.history_window: 1` (so a single phase-1 run is sufficient to trigger adjustment).

**Phase 1 — Stress:** Two jobs run under the `low-cpu` profile (1 CPU, 2g memory) and deliberately consume resources above the 80% threshold:
- `low-cpu-stress-cpu`: Runs a tight bash CPU loop for 30 seconds, driving CPU utilization above 80% of the 1-CPU limit
- `low-cpu-stress-mem`: Writes ~1.5GB to `/dev/shm` (RAM-backed tmpfs), pushing peak memory above 80% of the 2g limit (combined with the runner's ~300MB base usage)

Each phase-1 job also reads its cgroup limits and uploads them as artifacts for comparison.

After phase 1 completes, the system collects cgroup metrics (via `docker exec`) and records them to SQLite. The adjuster sees utilization above `scale_up_threshold` and will provision future runs with `baseline * scale_factor` resources.

**Phase 2 — Verify:** The same two job display names run again (via `needs: [stress]`). Because the `JobDisplayName` matches the phase-1 history, the adjuster overrides the baseline:
- `low-cpu-stress-cpu`: Should now have CPU limit > 1.0 (expected: 1.5 CPUs with scale_factor=1.5)
- `low-cpu-stress-mem`: Should now have memory limit > 2.0 GB (expected: 3.0 GB with scale_factor=1.5)

Each phase-2 job reads its cgroup limits, compares against the baseline, and reports pass/fail.

**Summary job:** Collects all artifacts from both phases and publishes a consolidated markdown table to the GitHub Actions step summary showing phase-1 baseline limits, phase-2 adjusted limits, and pass/fail verdicts.

### Config prerequisites for the test

```yaml
adaptive:
  enabled: true
  db_path: "metrics.db"
  scale_up_threshold: 0.80
  scale_down_threshold: 0.30
  scale_factor: 1.5
  history_window: 1          # Must be 1 for single-run test
  max_cpus: "16"
  max_memory: "32g"
```

### New file

```
.github/workflows/test-adaptive-scaling.yaml
```

## Dependencies

- `modernc.org/sqlite` — pure Go SQLite driver (no CGO, simpler cross-compilation)

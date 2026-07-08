# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This project implements intelligent GitHub Actions runner assignment via a proxy system. The core problem: GitHub Actions matrix workflows randomly assign runners with the same label — there's no native way to route jobs to specific runners based on hardware requirements (e.g., high-CPU vs low-CPU).

### Architecture

Two components run together (combined in `cmd/all/main.go`). Each component can also run standalone via `cmd/listener/main.go` and `cmd/proxy/main.go`.

1. **Listener/Scaler** (`internal/scaler/`) — Uses the [actions/scaleset Go SDK](https://github.com/actions/scaleset) `MessageSessionClient` directly (not `listener.Run()`) to receive per-job details. Classifies jobs by display name using glob patterns, generates JIT runner configs, and provisions Docker containers with matching CPU/memory limits.
2. **HTTP CONNECT Proxy** (`internal/proxy/`) — Intercepts all runner-to-GitHub HTTPS traffic. Identifies runners by container IP via the shared state store and logs runner name, profile, job name, and target host for every tunnel.

### Internal Packages

- **`internal/config/`** — Loads and validates `config.yaml`. Builds ordered profile list for deterministic glob matching. Checks that `default_profile` references an existing profile.
- **`internal/classifier/`** — Matches `JobDisplayName` against each profile's `match_patterns` using `filepath.Match` (glob). First match wins; falls back to `default_profile`.
- **`internal/state/`** — Thread-safe (`sync.RWMutex`) store tracking `RunnerInfo`: name, container ID/IP, profile, job ID/name, status (idle/busy/completed). Lookup by name, IP, or job ID.
- **`internal/runner/`** — Docker container lifecycle. Creates containers with `NanoCPUs`/`Memory` limits on a dedicated bridge network (`gh-proxy-runners`). Passes JIT config and proxy URL as env vars. Image: `ghcr.io/actions/actions-runner:latest` with `Cmd: ["/home/runner/run.sh"]` and `User: "runner"`.
- **`internal/scaler/`** — Custom message loop processing `JobAssigned`, `JobStarted`, `JobCompleted` messages. Uses `Statistics.TotalAssignedJobs` from each message to detect orphaned jobs (when GitHub assigns a different job to a runner than intended) and provisions additional runners to fill the gap.
- **`internal/metrics/`** — Adaptive resource scaling. Three components: `Store` (SQLite-backed history of per-job CPU/memory usage), `DockerCollector` (reads cgroup v2/v1 metrics from containers via `docker exec`), and `Adjuster` (pure function computing adjusted CPU/memory from baseline profile + historical usage). Thresholds, scale factor, history window, and ceilings are configured in the `adaptive` section of `config.yaml`.

### Key Design Decisions

- **Custom message loop** — The SDK's `listener.Run()` only exposes `HandleDesiredRunnerCount(count)` — a number, not individual job details. We use `MessageSessionClient.GetMessage()` directly to inspect `JobDisplayName` and classify each job.
- **Statistics-based reconciliation** — JIT configs don't lock runners to specific jobs. GitHub may send any queued job to any registered runner. The scaler tracks `Statistics.TotalAssignedJobs` and provisions additional runners when there's a deficit vs active runner count.
- **Message acknowledgment before processing** — Matches the official listener pattern. `DeleteMessage` is called before handling events to prevent re-delivery loops.
- **409 conflict handling** — On stale session (409), the scale set is deleted and recreated to get a fresh session.
- **Adaptive scaling at provisioning time** — The adjuster overrides resource values when creating containers, but `config.yaml` profiles remain the source of truth for baselines and floors. This keeps the config clean while allowing runtime optimization. SQLite stores usage history across restarts. The collector reads cgroup files (not Docker stats API) for Kubernetes portability.

## Workflow Expectations

- **Always update documentation** — When making code changes, update relevant documentation (README.md, CLAUDE.md, code comments) as part of the same task. Don't wait to be asked.

## Build and Run Commands

```bash
# Build
go build -o bin/gh-proxy ./cmd/all

# Run all tests
go test ./internal/...

# Run specific package tests
go test ./internal/scaler/...
go test ./internal/classifier/...

# Start the system (requires Docker running + GITHUB_TOKEN set)
export GITHUB_TOKEN=$(grep GH_TOKEN .env | cut -d= -f2)
./bin/gh-proxy --config config.yaml

# Trigger test workflow
gh workflow run test-case-10     # 10 jobs: 1 high-cpu at #4, 9 low-cpu

# Lint and format
go vet ./internal/...
prek run --all-files
```

## Configuration

`config.yaml` maps job display name glob patterns to resource profiles. Profiles are matched in order — first match wins.

The `GITHUB_TOKEN` env var must be set (PAT with `repo` + `admin:org` scopes). The `.env` file in the repo root stores it as `GH_TOKEN`.

## Test Infrastructure

One manually-triggered workflow (`workflow_dispatch`):

- **`test-case-10.yaml`** — 10-job matrix: 1 `high-cpu` (at position #4) + 9 `low-cpu-*`, all using `["gh-proxy-runner"]` label. Each job reads cgroup CPU/memory limits, validates against expected values, and uploads results as an artifact. A downstream `summary` job collects all artifacts and publishes a single consolidated markdown table to the GitHub Actions job summary with a pass/fail verdict.
- **`test-adaptive-scaling.yaml`** — Two-phase test: phase 1 stresses CPU/memory under `low-cpu` baseline, phase 2 verifies the adaptive system provisioned higher limits. Requires `adaptive.history_window: 1` in config. Summary job reports baseline vs adjusted limits.

Verification: check the `summary` job's GitHub Actions summary for the consolidated table. Also check logs for `runner_name=runner-high-cpu-*` on high-cpu jobs and `runner_name=runner-low-cpu-*` on low-cpu jobs. Zero mismatches = success.

## Environment

- **Go version** — `go 1.25.3` in `go.mod` (uses `GOTOOLCHAIN=auto` to auto-download)
- **Key dependencies** — `github.com/actions/scaleset v0.1.0`, `github.com/docker/docker v28.5.2`, `github.com/spf13/cobra`, `gopkg.in/yaml.v3`
- **Prek** — Configured via `.pre-commit-config.yaml` with betterleaks (secret scanning) and standard hooks (trailing whitespace, YAML validation, large file check). Install: `prek autoupdate && prek install`. Run manually: `prek run --all-files`.

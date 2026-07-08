# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This project implements intelligent GitHub Actions runner assignment via a proxy system. The core problem: GitHub Actions matrix workflows randomly assign runners with the same label — there's no native way to route jobs to specific runners based on hardware requirements (e.g., high-CPU vs low-CPU).

### Architecture

Two components run together (combined in `cmd/all/main.go`). Each component can also run standalone via `cmd/listener/main.go` and `cmd/proxy/main.go`.

1. **Listener/Scaler** (`internal/scaler/`) — Uses the [actions/scaleset Go SDK](https://github.com/actions/scaleset) `MessageSessionClient` directly (not `listener.Run()`) to receive per-job details. Classifies jobs by display name using glob patterns, generates JIT runner configs, and provisions Docker containers with matching CPU/memory limits.
2. **HTTP CONNECT Proxy** (`internal/proxy/`) — Intercepts all runner-to-GitHub HTTPS traffic. Identifies runners by container IP via the shared state store and logs runner name, profile, job name, and target host for every tunnel.

### Internal Packages

- **`internal/bootstrap/`** — Shared entry-point wiring: scaleset client + message session (including 409 stale-session recovery), runner group resolution, scale-set get-or-create, provisioner, adaptive-metrics components, and the scaler itself. `cmd/all` and `cmd/listener` both call `bootstrap.Setup`; construction logic lives here, not in `cmd/`.
- **`internal/config/`** — Loads and validates `config.yaml`. Builds ordered profile list for deterministic glob matching. Checks that `default_profile` references an existing profile.
- **`internal/classifier/`** — Matches `JobDisplayName` against each profile's `match_patterns` using `filepath.Match` (glob). First match wins; falls back to `default_profile`.
- **`internal/state/`** — Thread-safe (`sync.RWMutex`) store tracking `RunnerInfo`: name, container ID/IP, profile, job ID/name, status (idle/busy). Lookup by name or container IP (IP lookups are O(1) via a secondary index — the proxy's per-connection hot path). Completed runners are removed, so `ActiveCount` is simply the tracked-runner count.
- **`internal/runner/`** — Docker container lifecycle. Creates containers with `NanoCPUs`/`Memory` limits on a dedicated bridge network (`gh-proxy-runners`). Passes JIT config and proxy URL as env vars. Image: `ghcr.io/actions/actions-runner:latest` (pulled only if not present locally) with `Cmd: ["/home/runner/run.sh"]` and `User: "runner"`. A post-job grace period (passed by bootstrap, 15s when adaptive scaling is on, 0 otherwise) keeps containers alive after the runner exits so the metrics collector can read cgroup files.
- **`internal/scaler/`** — Custom message loop processing `JobAssigned`, `JobStarted`, `JobCompleted` messages. Uses `Statistics.TotalAssignedJobs` from each message to detect orphaned jobs (when GitHub assigns a different job to a runner than intended) and provisions additional runners to fill the gap. Synthetic reconcile runners have an empty job display name — they use the baseline profile and never record metrics history. Job-completion cleanup (metrics collection + container stop) runs in goroutines off the message loop; `Run` waits for them before returning. `GetMessage` failures back off exponentially (1s→30s).
- **`internal/metrics/`** — Adaptive resource scaling. Three components: `Store` (SQLite-backed history of per-job CPU/memory usage), `DockerCollector` (reads cgroup v2/v1 metrics from containers via `docker exec`), and `Adjuster` (pure function computing adjusted CPU/memory from baseline profile + historical usage; build with `NewAdjuster(cfg.Adaptive)`). Thresholds, scale factor, history window, and ceilings are configured in the `adaptive` section of `config.yaml`.
- **`internal/units/`** — Single owner of CPU/memory string formats ("4", "1.5", "8g", "512m"): `ParseCPU`/`ParseMemory`/`FormatCPU`/`FormatMemory`. No other package parses or formats resource strings.
- **`internal/dockerutil/`** — Shared Docker helpers: `NewClient()` (env + API version negotiation) and `ShortID()` (12-char container IDs for logging).

### Key Design Decisions

- **Custom message loop** — The SDK's `listener.Run()` only exposes `HandleDesiredRunnerCount(count)` — a number, not individual job details. We use `MessageSessionClient.GetMessage()` directly to inspect `JobDisplayName` and classify each job.
- **Statistics-based reconciliation** — JIT configs don't lock runners to specific jobs. GitHub may send any queued job to any registered runner. The scaler tracks `Statistics.TotalAssignedJobs` and provisions additional runners when there's a deficit vs active runner count.
- **Message acknowledgment before processing** — Matches the official listener pattern. `DeleteMessage` is called before handling events to prevent re-delivery loops.
- **409 conflict handling** — On stale session (409), the scale set is deleted and recreated to get a fresh session.
- **Adaptive scaling at provisioning time** — The adjuster overrides resource values when creating containers, but `config.yaml` profiles remain the source of truth for baselines and floors. This keeps the config clean while allowing runtime optimization. SQLite stores usage history across restarts. The collector reads cgroup files (not Docker stats API) for Kubernetes portability.

## Workflow Expectations

- **Always update documentation** — When making code changes, update relevant documentation (README.md, CLAUDE.md, code comments) as part of the same task. Don't wait to be asked.

## Build and Run Commands

Prefer the `justfile` targets:

```bash
just build                        # go build -o bin/gh-proxy ./cmd/all
just test                         # go test ./...
just lint                         # go vet + prek run --all-files
just run                          # start the system (requires Docker + GH_TOKEN in .env)
just e2e                          # full e2e: test-case-10 (see Test Infrastructure)
just e2e test-adaptive-scaling    # full e2e: adaptive scaling
```

Raw equivalents:

```bash
go build -o bin/gh-proxy ./cmd/all
go test ./internal/...
export GITHUB_TOKEN=$(grep GH_TOKEN .env | cut -d= -f2)
./bin/gh-proxy --config config.yaml
gh workflow run test-case-10     # 10 jobs: 1 high-cpu at #4, 9 low-cpu
go vet ./internal/...
prek run --all-files
```

## Configuration

`config.yaml` maps job display name glob patterns to resource profiles. Profiles are matched in order — first match wins.

The `GITHUB_TOKEN` env var must be set for `cmd/all` and `cmd/listener` (PAT with `repo` + `admin:org` scopes); it is checked in `bootstrap.Setup`, not config validation, so the standalone proxy doesn't require it. The `.env` file in the repo root stores it as `GH_TOKEN`.

## Test Infrastructure

**Run e2e tests with `just e2e <workflow>`** — `scripts/e2e.sh` handles preflight checks (Docker, gh auth, config), builds and starts the system, dispatches the workflow, waits for the conclusion, prints per-job results plus runner assignments, and tears down. Exit 0 = pass. Logs/artifacts land in `.e2e/<timestamp>-<workflow>/`. Note `gh workflow run` uses the workflow file from the **remote** ref — workflow edits need commit+push before they take effect (the harness warns about this).

Two manually-triggered workflows (`workflow_dispatch`):

- **`test-case-10.yaml`** — 10-job matrix: 1 `high-cpu` (at position #4) + 9 `low-cpu-*`, all using `["gh-proxy-runner"]` label. Each job reads cgroup CPU/memory limits (via the shared `./.github/actions/read-limits` composite action), validates against expected values, and uploads results as an artifact. A downstream `summary` job collects all artifacts and publishes a single consolidated markdown table to the GitHub Actions job summary with a pass/fail verdict.
- **`test-adaptive-scaling.yaml`** — Two-phase test: phase 1 stresses CPU/memory under `low-cpu` baseline, a `settle` job waits 30s for the listener to record phase-1 metrics, phase 2 verifies the adaptive system provisioned higher limits. Requires `adaptive.enabled: true` and `adaptive.history_window: 1` in config, and a fresh `metrics.db` (the harness enforces all three). Summary job reports baseline vs adjusted limits.

**Conventions for new e2e workflows** (documented in README "End-to-end testing"): matrix job names must match profile `match_patterns`; use the `read-limits` composite action instead of copy-pasting cgroup detection; always end with a gate job that exits nonzero on failed assertions — the harness's verdict is the run conclusion. Lint with actionlint (`go run github.com/rhysd/actionlint/cmd/actionlint@latest .github/workflows/*.yaml`; custom label declared in `.github/actionlint.yaml`).

Manual verification (when not using the harness): check the `summary` job's GitHub Actions summary for the consolidated table. Also check logs for `runner_name=runner-high-cpu-*` on high-cpu jobs and `runner_name=runner-low-cpu-*` on low-cpu jobs. Zero mismatches = success.

## Environment

- **Go version** — `go 1.25.3` in `go.mod` (uses `GOTOOLCHAIN=auto` to auto-download)
- **Key dependencies** — `github.com/actions/scaleset v0.1.0`, `github.com/docker/docker v28.5.2`, `github.com/spf13/cobra`, `gopkg.in/yaml.v3`
- **Prek** — Configured via `.pre-commit-config.yaml` with betterleaks (secret scanning) and standard hooks (trailing whitespace, YAML validation, large file check). Install: `prek autoupdate && prek install`. Run manually: `prek run --all-files`.

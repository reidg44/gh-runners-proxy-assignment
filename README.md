# gh-runners-proxy-assignment

Intelligent job-to-runner routing for GitHub Actions. Routes matrix workflow jobs to Docker containers with matching hardware profiles (CPU, memory) based on job display names — something GitHub doesn't natively support.

## Problem

GitHub Actions matrix workflows with self-hosted runners randomly assign jobs to any runner with a matching label. There's no way to say "this job needs 4 CPUs" and have GitHub pick the right runner. If you have jobs with different resource requirements sharing the same label, assignment is a coin flip.

## Solution

This project sits between GitHub and the runners. It listens for job events via the [actions/scaleset SDK](https://github.com/actions/scaleset), classifies each job by its display name using glob patterns, and provisions a JIT (just-in-time) Docker container with the right CPU/memory limits. An HTTP CONNECT proxy logs all runner-to-GitHub traffic with runner/profile identification.

### How it works

1. GitHub sends a `JobAssigned` message for each queued job
2. The **classifier** matches the job's display name against glob patterns in `config.yaml` (e.g., `high-cpu*` → 4 CPUs, `low-cpu*` → 1 CPU)
3. The **scaler** generates a JIT runner config and starts a Docker container with the matching resource profile
4. The runner registers with GitHub, executes its job, and exits
5. The **proxy** intercepts all HTTPS traffic from runners, identifying each by container IP and logging the runner name, profile, and target host
6. On job completion, the container is stopped and removed

### Architecture

```
GitHub Actions
     │
     ▼
┌─────────────┐    JobAssigned/Started/Completed messages
│  Scaleset    │◄──────────────────────────────────────────
│  SDK         │
└──────┬──────┘
       │ classify job name → profile
       ▼
┌─────────────┐    JIT config + docker create
│  Runner      │──────────────────────────────►  Docker containers
│  Provisioner │                                 (CPU/mem limits)
└──────────────┘                                      │
                                                      │ HTTPS traffic
                                                      ▼
                                                ┌───────────┐
                                                │  HTTP      │
                                                │  CONNECT   │
                                                │  Proxy     │
                                                └───────────┘
```

## Project Structure

```
cmd/
  all/main.go              Combined entry point (listener + proxy)
  listener/main.go         Listener-only entry point
  proxy/main.go            Proxy-only entry point
internal/
  classifier/              Job display name → profile matching (glob)
  config/                  YAML config loading and validation
  proxy/                   HTTP CONNECT proxy with runner identification
  runner/                  Docker container lifecycle (create, stop, network)
  scaler/                  Custom message loop, job dispatch, reconciliation
  state/                   Thread-safe runner state store
config.yaml                Job-to-profile mapping configuration
.github/workflows/
  test-case.yaml           8-job test workflow (1 high-cpu, 7 low-cpu)
  test-case-10.yaml        10-job test workflow (1 high-cpu at #4, 9 low-cpu)
```

## Configuration

`config.yaml` maps job display name patterns to resource profiles:

```yaml
github:
  repository_url: "https://github.com/your-org/your-repo"
  scale_set_name: "gh-proxy-runner-scaleset"
  runner_label: "gh-proxy-runner"
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
```

**Profiles** are matched in the order they appear. First matching glob wins. Jobs that don't match any pattern get the `default_profile`.

## Prerequisites

- **Docker** — must be running (the devcontainer uses docker-in-docker)
- **Go 1.22+** — included in the devcontainer
- **GitHub PAT** — with `repo` and `admin:org` scopes, stored in `.env` as `GH_TOKEN`

The devcontainer is pre-configured with Go, Docker-in-Docker, and privileged mode. Opening this repo in VS Code with the Dev Containers extension will set everything up.

## Building

```bash
go build -o bin/gh-proxy ./cmd/all
```

## Running

```bash
# Set the GitHub token
export GITHUB_TOKEN=$(grep GH_TOKEN .env | cut -d= -f2)

# Start the combined listener + proxy
./bin/gh-proxy --config config.yaml
```

The system will:
1. Start the HTTP CONNECT proxy on the configured port (`:8080`)
2. Connect to GitHub and create/reuse a runner scale set
3. Pull the runner Docker image
4. Create a dedicated bridge network (`gh-proxy-runners`)
5. Begin polling for job messages

## Testing

Two test workflows are included. Both use `workflow_dispatch` (manual trigger).

### test-case (8 jobs)

1 `high-cpu` + 7 `low-cpu-*` jobs, all with the `["gh-proxy-runner"]` label.

```bash
# With the system running:
gh workflow run test-case
gh run watch  # watch it complete
```

### test-case-10 (10 jobs)

1 `high-cpu` (at position #4) + 9 `low-cpu-*` jobs.

```bash
gh workflow run test-case-10
gh run watch
```

### Verifying correct routing

Check the logs for profile assignments:

```bash
# Every completed job shows its runner name and profile
grep "job completed.*result=succeeded" /tmp/gh-proxy.log

# Verify high-cpu jobs always land on high-cpu runners
grep "job completed.*high-cpu.*result=succeeded" /tmp/gh-proxy.log
# Should show: runner_name=runner-high-cpu-*

# Verify low-cpu jobs always land on low-cpu runners
grep "job completed.*low-cpu.*result=succeeded" /tmp/gh-proxy.log
# Should show: runner_name=runner-low-cpu-*
```

The proxy also logs every HTTPS tunnel with runner identification:

```
CONNECT tunnel runner_name=runner-high-cpu-xxx profile=high-cpu target=github.com:443
```

### Running unit tests

```bash
go test ./internal/...
```

## Key Design Decisions

**Custom message loop instead of `listener.Run()`** — The SDK's built-in listener only exposes `HandleDesiredRunnerCount(count int)`, which gives a number, not per-job details. We use `MessageSessionClient.GetMessage()` directly to inspect `JobDisplayName` from each `JobAssigned` message and classify jobs before provisioning.

**Statistics-based reconciliation** — GitHub may assign a different job to a runner than the one from the `JobAssigned` message (JIT configs don't lock runners to specific jobs). The scaler uses `Statistics.TotalAssignedJobs` from each message to detect orphaned jobs and provision additional runners to fill the gap.

**JIT runners** — Each runner is ephemeral. It registers with GitHub, runs one job, and exits. The container is stopped and removed after job completion.

**Bridge network** — All runner containers join a dedicated Docker bridge network. The proxy is reachable via the gateway IP, which is passed to containers as `http_proxy`/`https_proxy`.

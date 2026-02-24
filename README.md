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
  test-case-10.yaml        10-job matrix workflow (1 high-cpu at #4, 9 low-cpu)
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

A test workflow is included using `workflow_dispatch` (manual trigger).

### test-case-10 (10 jobs)

1 `high-cpu` (at position #4) + 9 `low-cpu-*` jobs, all with the `["gh-proxy-runner"]` label. Uses a matrix strategy so each job's `name` field becomes its `JobDisplayName` for classification.

Each job self-validates by reading cgroup CPU/memory limits and comparing them against expected values based on its name. Results are uploaded as artifacts. A downstream `summary` job collects all results and publishes a single consolidated markdown table to the GitHub Actions job summary with a pass/fail verdict — making it easy to verify all 10 jobs at a glance.

```bash
# With the system running:
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

## How the Proxy Works

The proxy is the core mechanism that makes resource-aware routing possible. It's the link between "we provisioned a container with 4 CPUs" and "we can prove that the high-cpu job actually ran on that container."

### The Problem the Proxy Solves

GitHub's runner protocol is simple: a runner registers, GitHub sends it a job, the runner executes it. All communication is HTTPS from the runner to GitHub — there's no inbound connection for us to intercept. We can't insert ourselves into the GitHub→runner assignment path. But we *can* insert ourselves into the runner→GitHub traffic path by controlling the network.

### Network Architecture

At startup, the system creates a dedicated Docker bridge network (`gh-proxy-runners`). Every runner container is attached to this network. The proxy server binds to port 8080 on the host.

```
┌─────────────────────────────────────────────────────────────┐
│  Docker bridge network: gh-proxy-runners                    │
│                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │ runner-high-  │  │ runner-low-  │  │ runner-low-  │ ...  │
│  │ cpu-job-1     │  │ cpu-job-2    │  │ cpu-job-3    │      │
│  │ 172.18.0.2    │  │ 172.18.0.3   │  │ 172.18.0.4   │      │
│  │ CPUs: 4       │  │ CPUs: 1      │  │ CPUs: 1      │      │
│  │ Mem: 8GB      │  │ Mem: 2GB     │  │ Mem: 2GB     │      │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘      │
│         │                 │                 │               │
│         ▼                 ▼                 ▼               │
│  ┌──────────────────────────────────────────────────┐       │
│  │          Gateway IP: 172.18.0.1                  │       │
│  │          HTTP CONNECT Proxy (:8080)              │       │
│  └──────────────────────┬───────────────────────────┘       │
└─────────────────────────┼───────────────────────────────────┘
                          │
                          ▼
                   github.com:443
                   (pipelines.actions.githubusercontent.com, etc.)
```

The key trick: when each runner container is created, it's injected with proxy environment variables pointing to the bridge network's gateway IP:

```
https_proxy=http://172.18.0.1:8080
http_proxy=http://172.18.0.1:8080
HTTPS_PROXY=http://172.18.0.1:8080
HTTP_PROXY=http://172.18.0.1:8080
```

This forces **all** HTTP/HTTPS traffic from every runner through our proxy. The runner doesn't know it's being proxied — it just follows standard proxy environment variables that virtually all HTTP clients respect.

### Runner Identification via Source IP

When a runner makes an HTTPS request (e.g., to `github.com:443`), it arrives at the proxy as an HTTP CONNECT request. The proxy extracts the source IP from the TCP connection:

```
source IP: 172.18.0.2 → lookup in state store → runner-high-cpu-job-1, profile=high-cpu
```

The **state store** (`internal/state/state.go`) is the shared data structure that connects the scaler and proxy. When the scaler provisions a runner container, it records the container's bridge network IP in the store alongside the runner name, profile, job ID, and job display name. When the proxy receives a connection, it calls `store.GetByContainerIP(sourceIP)` to look up exactly which runner — and which resource profile — is making the request.

This is what a proxy log line looks like:

```
CONNECT tunnel runner_name=runner-high-cpu-job-1 profile=high-cpu job_name=high-cpu target=github.com:443 source_ip=172.18.0.2
```

Every HTTPS connection from every runner is logged with full attribution: which runner, which profile, which job, and where it's connecting. This creates a complete audit trail of runner-to-job assignments.

### HTTP CONNECT Tunnel Mechanism

The proxy implements the [HTTP CONNECT method](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Methods/CONNECT) — the standard way HTTP proxies handle HTTPS traffic:

1. The runner sends `CONNECT github.com:443 HTTP/1.1`
2. The proxy opens a TCP connection to `github.com:443`
3. The proxy responds `HTTP/1.1 200 Connection Established`
4. The proxy hijacks the HTTP connection to get the raw TCP socket
5. Two goroutines shuttle bytes bidirectionally: runner ↔ proxy ↔ GitHub

The proxy never decrypts TLS — it's a transparent tunnel. It can't see the content of requests, but it doesn't need to. The source IP identification tells it everything: which runner is talking, what profile it has, and which job it's running.

### Shared State: The Glue Between Scaler and Proxy

The scaler and proxy run in the same process and share a single thread-safe `state.Store`. The data flow:

```
Scaler                          State Store                       Proxy
  │                                 │                               │
  │  1. Provision runner            │                               │
  │     container_ip=172.18.0.2     │                               │
  │  ──── AddRunner(info) ────────► │                               │
  │                                 │  2. Runner makes HTTPS req    │
  │                                 │ ◄── GetByContainerIP(ip) ──── │
  │                                 │ ─── RunnerInfo ─────────────► │
  │                                 │                               │
  │                                 │     3. Proxy logs:            │
  │                                 │        runner=runner-high-cpu  │
  │                                 │        profile=high-cpu       │
  │                                 │        target=github.com:443  │
  │  4. Job completes               │                               │
  │  ──── Remove(name) ───────────► │                               │
```

The store tracks each runner's full lifecycle: idle (provisioned, waiting for job), busy (executing a job), and completed (job finished). This allows the proxy to identify runners at any point during their lifecycle.

### Why This Matters

Without the proxy, we'd have no way to verify that our resource-aware provisioning actually works. We could provision a 4-CPU container for a high-cpu job, but GitHub might silently reassign that job to a different runner. The proxy gives us:

1. **Verification** — Every request is logged with the runner's profile, so we can confirm high-cpu jobs always flow through high-cpu runners
2. **Observability** — A real-time view of which runners are active, what profiles they have, and what they're connecting to
3. **Audit trail** — Complete history of all runner-to-GitHub communication with runner/profile attribution

## Key Design Decisions

**Custom message loop instead of `listener.Run()`** — The SDK's built-in listener only exposes `HandleDesiredRunnerCount(count int)`, which gives a number, not per-job details. We use `MessageSessionClient.GetMessage()` directly to inspect `JobDisplayName` from each `JobAssigned` message and classify jobs before provisioning.

**Statistics-based reconciliation** — GitHub may assign a different job to a runner than the one from the `JobAssigned` message (JIT configs don't lock runners to specific jobs). The scaler uses `Statistics.TotalAssignedJobs` from each message to detect orphaned jobs and provision additional runners to fill the gap.

**JIT runners** — Each runner is ephemeral. It registers with GitHub, runs one job, and exits. The container is stopped and removed after job completion.

**Bridge network** — All runner containers join a dedicated Docker bridge network. The proxy is reachable via the gateway IP, which is passed to containers as `http_proxy`/`https_proxy`.

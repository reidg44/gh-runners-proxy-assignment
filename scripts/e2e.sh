#!/usr/bin/env bash
# End-to-end test harness.
#
# Builds the binary, starts the full system (listener + proxy + Docker
# provisioning), dispatches a GitHub Actions workflow, waits for it to
# finish, and reports a single PASS/FAIL verdict. The verdict is the
# workflow run's conclusion — every e2e workflow must end in a summary/gate
# job that exits nonzero when any assertion fails (see README "End-to-end
# testing" for the conventions).
#
# Usage: scripts/e2e.sh <workflow-name> [--keep-metrics] [--input k=v ...]
#   --keep-metrics  keep the existing metrics.db. By default it is moved
#                   aside (saved in the run dir) so adaptive scaling starts
#                   from a clean history — leftover history makes the
#                   adjuster override the static profile limits that
#                   test workflows assert against. Not allowed for
#                   test-adaptive-scaling, whose baselines require a
#                   fresh history.
#   --input k=v     pass a workflow_dispatch input (repeatable), e.g.
#                   --input total_jobs=60 for test-stress.
#
# Environment: E2E_TIMEOUT (seconds, default 1800) caps the wait for the
# workflow run.
set -euo pipefail

# macOS: prevent idle sleep for the duration of the run. A host suspend
# freezes Docker and the listener mid-run and manifests as a mysterious
# stall — observed live: two 60-job runs "deadlocked" (runners exited with
# stale broker sessions, jobs orphaned) because the laptop went to sleep.
if [ "$(uname)" = "Darwin" ] && [ -z "${E2E_CAFFEINATED:-}" ] && command -v caffeinate >/dev/null 2>&1; then
  E2E_CAFFEINATED=1 exec caffeinate -i "$0" "$@"
fi

WORKFLOW="${1:?usage: e2e.sh <workflow-name> [--keep-metrics] [--input k=v ...]}"
shift
FRESH_METRICS=true
INPUT_FLAGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --keep-metrics) FRESH_METRICS=false ;;
    --input)
      shift
      [ $# -gt 0 ] || { echo "--input requires key=value" >&2; exit 2; }
      INPUT_FLAGS+=(-f "$1")
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN_DIR="$ROOT/.e2e/$(date +%Y%m%d-%H%M%S)-$WORKFLOW"
mkdir -p "$RUN_DIR"

log()  { printf '\033[1;34m[e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[e2e] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

# ── Preflight ────────────────────────────────────────────────────────────
command -v gh >/dev/null 2>&1 || fail "gh CLI not installed"
gh auth status >/dev/null 2>&1 || fail "gh CLI not authenticated (run: gh auth login)"
docker info >/dev/null 2>&1 || fail "Docker is not running"

# Token: prefer GH_TOKEN from .env, but validate it — an expired PAT would
# otherwise surface as a confusing 401 mid-startup. Fall back to the gh CLI
# keyring token, which the preflight above already proved works.
GITHUB_TOKEN="$(grep '^GH_TOKEN' "$ROOT/.env" 2>/dev/null | cut -d= -f2- || true)"
if [ -n "$GITHUB_TOKEN" ]; then
  if ! curl -fsS -o /dev/null -H "Authorization: Bearer $GITHUB_TOKEN" https://api.github.com/user 2>/dev/null; then
    log "WARNING: GH_TOKEN in .env is invalid or expired (rotate it) — falling back to the gh CLI token"
    GITHUB_TOKEN=""
  fi
fi
if [ -z "$GITHUB_TOKEN" ]; then
  GITHUB_TOKEN="$(gh auth token)"
fi
export GITHUB_TOKEN

BRANCH=$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)
git -C "$ROOT" ls-remote --exit-code --heads origin "$BRANCH" >/dev/null 2>&1 \
  || fail "branch $BRANCH not on origin — push it, or dispatch runs the wrong workflow version"
if ! git -C "$ROOT" diff --quiet origin/"$BRANCH" -- .github/ 2>/dev/null; then
  log "WARNING: local .github/ differs from origin/$BRANCH — the dispatched run uses the REMOTE workflow files"
fi

# Workflow-specific preflight
if [ "$WORKFLOW" = "test-adaptive-scaling" ]; then
  grep -Eq '^\s*enabled:\s*true' "$ROOT/config.yaml" \
    || fail "test-adaptive-scaling requires adaptive.enabled: true in config.yaml"
  grep -Eq '^\s*history_window:\s*1\b' "$ROOT/config.yaml" \
    || fail "test-adaptive-scaling requires adaptive.history_window: 1 in config.yaml"
  $FRESH_METRICS || fail "test-adaptive-scaling cannot run with --keep-metrics: its phase-1 baselines require a fresh history"
fi

# Remove stale runner containers from a previous (crashed) run
STALE=$(docker ps -aq --filter network=gh-proxy-runners 2>/dev/null || true)
if [ -n "$STALE" ]; then
  log "removing stale runner containers from a previous run"
  echo "$STALE" | xargs docker rm -f >/dev/null
fi

if $FRESH_METRICS && [ -f "$ROOT/metrics.db" ]; then
  mv "$ROOT/metrics.db" "$RUN_DIR/metrics.db.pre-run"
  log "moved metrics.db aside for a deterministic run (saved in $RUN_DIR)"
fi

# ── Build & start ────────────────────────────────────────────────────────
log "building bin/gh-proxy"
(cd "$ROOT" && go build -o bin/gh-proxy ./cmd/all)

log "starting gh-proxy (log: $RUN_DIR/gh-proxy.log)"
"$ROOT/bin/gh-proxy" --config "$ROOT/config.yaml" >"$RUN_DIR/gh-proxy.log" 2>&1 &
PROXY_PID=$!

MONITOR_PID=""
cleanup() {
  [ -n "$MONITOR_PID" ] && kill "$MONITOR_PID" 2>/dev/null || true
  log "shutting down gh-proxy (pid $PROXY_PID)"
  kill "$PROXY_PID" 2>/dev/null || true
  # Graceful shutdown stops runner containers; wait for it, then force-clean.
  for _ in $(seq 1 20); do
    kill -0 "$PROXY_PID" 2>/dev/null || break
    sleep 1
  done
  kill -9 "$PROXY_PID" 2>/dev/null || true
  LEFTOVER=$(docker ps -aq --filter network=gh-proxy-runners 2>/dev/null || true)
  if [ -n "$LEFTOVER" ]; then
    log "force-removing leftover containers"
    echo "$LEFTOVER" | xargs docker rm -f >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

log "waiting for the scaler to establish a message session"
READY=""
for _ in $(seq 1 60); do
  if grep -q "scaler started" "$RUN_DIR/gh-proxy.log" 2>/dev/null; then
    READY=1
    break
  fi
  if ! kill -0 "$PROXY_PID" 2>/dev/null; then
    tail -20 "$RUN_DIR/gh-proxy.log" >&2
    fail "gh-proxy exited during startup"
  fi
  sleep 1
done
if [ -z "$READY" ]; then
  tail -20 "$RUN_DIR/gh-proxy.log" >&2
  fail "scaler not ready after 60s"
fi
log "system ready"

# Sample concurrent runner-container counts every 5s. Peak vs max_runners
# catches reconciliation over-provisioning (the reconcile loop has no
# max_runners cap) — a purely local signal no workflow assertion can see.
(
  while :; do
    printf '%s,%s\n' "$(date +%s)" "$(docker ps -q --filter network=gh-proxy-runners 2>/dev/null | wc -l | tr -d ' ')"
    sleep 5
  done
) >"$RUN_DIR/containers.csv" &
MONITOR_PID=$!

# ── Dispatch and identify the run ────────────────────────────────────────
PREV_RUN=$(gh run list --workflow "$WORKFLOW" --limit 1 --json databaseId -q '.[0].databaseId' 2>/dev/null || echo "")
log "dispatching $WORKFLOW on ref $BRANCH"
# ${arr[@]+...} keeps empty-array expansion safe under set -u on bash 3.2 (macOS).
gh workflow run "$WORKFLOW" --ref "$BRANCH" ${INPUT_FLAGS[@]+"${INPUT_FLAGS[@]}"}

RUN_ID=""
for _ in $(seq 1 30); do
  RUN_ID=$(gh run list --workflow "$WORKFLOW" --limit 1 --json databaseId -q '.[0].databaseId' 2>/dev/null || echo "")
  if [ -n "$RUN_ID" ] && [ "$RUN_ID" != "$PREV_RUN" ]; then
    break
  fi
  RUN_ID=""
  sleep 2
done
[ -n "$RUN_ID" ] || fail "dispatched run did not appear within 60s"
RUN_URL=$(gh run view "$RUN_ID" --json url -q .url)
log "run $RUN_ID started: $RUN_URL"

# ── Wait for completion ──────────────────────────────────────────────────
DEADLINE=$((SECONDS + ${E2E_TIMEOUT:-1800}))
while :; do
  STATUS=$(gh run view "$RUN_ID" --json status -q .status)
  if [ "$STATUS" = "completed" ]; then
    break
  fi
  if [ "$SECONDS" -ge "$DEADLINE" ]; then
    gh run cancel "$RUN_ID" >/dev/null 2>&1 || true
    fail "run did not complete within ${E2E_TIMEOUT:-1800}s — cancelled. $RUN_URL"
  fi
  DONE=$(gh run view "$RUN_ID" --json jobs -q '[.jobs[] | select(.status == "completed")] | length')
  TOTAL=$(gh run view "$RUN_ID" --json jobs -q '.jobs | length')
  log "in progress — ${DONE}/${TOTAL} jobs completed ($((SECONDS))s elapsed)"
  sleep 15
done

CONCLUSION=$(gh run view "$RUN_ID" --json conclusion -q .conclusion)

# ── Report ───────────────────────────────────────────────────────────────
gh run view "$RUN_ID" --json jobs,conclusion,url >"$RUN_DIR/run.json"
gh run download "$RUN_ID" -D "$RUN_DIR/artifacts" >/dev/null 2>&1 || true

echo
log "per-job results:"
gh run view "$RUN_ID" --json jobs \
  -q '.jobs[] | "  \(.conclusion)\t\(.name)"' | sort

echo
log "container concurrency (sampled every 5s):"
PEAK=$(cut -d, -f2 "$RUN_DIR/containers.csv" 2>/dev/null | sort -n | tail -1)
PEAK="${PEAK:-0}"
# [[:space:]] not \s — macOS awk has no \s
MAX_RUNNERS=$(awk '/^[[:space:]]*max_runners:/ {print $2}' "$ROOT/config.yaml")
echo "  peak concurrent runner containers: $PEAK (config max_runners: ${MAX_RUNNERS:-?})"
if [ -n "$MAX_RUNNERS" ] && [ "$PEAK" -gt "$MAX_RUNNERS" ]; then
  log "WARNING: peak container count exceeded max_runners — the scaler over-provisioned (likely the uncapped reconcile loop)"
fi

echo
log "runner assignments (from local listener log):"
grep '"job started"' "$RUN_DIR/gh-proxy.log" 2>/dev/null \
  | sed -E 's/.*runner_name=([^ ]+).*job_display_name=([^ ]+).*/  \2 -> \1/' \
  | sort || echo "  (none recorded)"

echo
if [ "$CONCLUSION" = "success" ]; then
  log "PASS — $WORKFLOW succeeded. $RUN_URL"
  log "logs and artifacts saved in $RUN_DIR"
else
  log "workflow concluded: $CONCLUSION"
  log "last 30 lines of gh-proxy.log:"
  tail -30 "$RUN_DIR/gh-proxy.log" >&2
  fail "$WORKFLOW did not pass. Full logs: $RUN_DIR — run details: $RUN_URL"
fi

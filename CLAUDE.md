# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This project implements intelligent GitHub Actions runner assignment via a proxy system. The core problem: GitHub Actions matrix workflows randomly assign runners with the same label — there's no native way to route jobs to specific runners based on hardware requirements (e.g., high-CPU vs low-CPU).

### Two-Component Architecture

1. **Listener Server** — Uses the [actions/scaleset Go SDK](https://github.com/actions/scaleset) to receive job events from GitHub and spin up JIT (just-in-time) runners matching expected job configurations.
2. **Proxy Server** — Routes job requests to appropriate runners based on hardware specs, acting as an intermediary that manipulates traffic to self-hosted runners.

### Flow

When N jobs trigger in parallel with varying resource needs, the listener provisions runners with matching specs, and the proxy ensures each job lands on the correct runner (e.g., a high-CPU job goes to a powerful runner, not a standard one).

## Test Infrastructure

The test case workflow (`.github/workflows/test-case.yaml`) is manually triggered (`workflow_dispatch`) and runs 8 parallel jobs — 1 `high-cpu` and 7 `low-cpu-*` — all using the `["gh-proxy-runner"]` label. Verification is done through GitHub Actions logs confirming correct runner assignments.

## Current State

The project is in the planning/infrastructure stage. No implementation code exists yet. The `.gitignore` is configured for macOS and Python environments.

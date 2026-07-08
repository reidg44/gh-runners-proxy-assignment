# GitHub runner proxy — project commands. Run `just` to list.

set shell := ["bash", "-cu"]

default:
    @just --list

# Build the combined listener+proxy binary
build:
    go build -o bin/gh-proxy ./cmd/all

# Run unit tests
test:
    go test ./...

# Vet + hooks (secret scan, whitespace, yaml)
lint:
    go vet ./...
    prek run --all-files

# Start the system locally (requires Docker running + GH_TOKEN in .env)
run: build
    GITHUB_TOKEN=$(grep '^GH_TOKEN' .env | cut -d= -f2) ./bin/gh-proxy --config config.yaml

# Run one e2e test: build + start system + dispatch workflow + report verdict (--fresh-metrics resets metrics.db)
e2e workflow="test-case-10" *flags="":
    scripts/e2e.sh {{workflow}} {{flags}}

# Run every e2e workflow in sequence
e2e-all:
    just e2e test-case-10
    just e2e test-adaptive-scaling

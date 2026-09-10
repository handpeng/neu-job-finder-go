#!/usr/bin/env bash
set -euo pipefail
echo "Starting NEU Job Finder on http://localhost:8080"
go test ./...
go run ./cmd/server

$ErrorActionPreference = "Stop"
Write-Host "Starting NEU Job Finder on http://localhost:8080" -ForegroundColor Green
go test ./...
go run ./cmd/server

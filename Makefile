run:
	go run ./cmd/api

tidy:
	go mod tidy

# The server with its commit and build time (shown by /status in the bot).
build:
	go build -ldflags "-X github.com/bnursik/business_surgery_backend/internal/buildinfo.Commit=$$(git rev-parse HEAD 2>/dev/null) -X github.com/bnursik/business_surgery_backend/internal/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/api ./cmd/api

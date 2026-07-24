# Strato — build & development entry points.
SHELL := /bin/sh

BIN_DIR      := bin
COMPOSE      := docker compose -f docker/docker-compose.yml
GO           := go
GOFLAGS      := -trimpath
LDFLAGS      := -s -w

.PHONY: all help proto build run worker migrate seed lint test test-integration test-e2e bench cover \
        dev-up dev-down docker-build clean tidy

all: build

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

proto: ## Generate gRPC/gateway/OpenAPI code from proto/ (requires buf)
	buf generate

tidy: ## Sync go.mod/go.sum
	$(GO) mod tidy

build: proto ## Build all binaries into ./bin
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/server ./cmd/server
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/worker ./cmd/worker
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/migrate ./cmd/migrate

run: ## Run the API server locally
	$(GO) run ./cmd/server --config configs/config.yaml

worker: ## Run the GC worker locally
	$(GO) run ./cmd/worker --config configs/config.yaml

migrate: ## Apply database migrations
	$(GO) run ./cmd/migrate --config configs/config.yaml

seed: ## Load development seed data
	$(COMPOSE) exec -T postgres psql -U strato -d strato < scripts/seed.sql

lint: ## Run golangci-lint and buf lint
	golangci-lint run ./...
	buf lint

test: ## Unit tests with race detector
	$(GO) test -race -count=1 ./...

test-integration: ## Repository tests against real Postgres (make dev-up first)
	$(GO) test -race -count=1 -tags integration ./tests/integration/...

test-e2e: ## API tests against a running server
	$(GO) test -count=1 -tags e2e ./tests/api/...

bench: ## Benchmarks (crypto throughput etc.)
	$(GO) test -bench . -benchmem -run '^$$' ./pkg/...

cover: ## Unit test coverage report
	$(GO) test -race -coverprofile=coverage.txt -covermode=atomic ./...
	$(GO) tool cover -html=coverage.txt -o coverage.html
	@echo "open coverage.html"

dev-up: ## Start postgres, redis, minio, prometheus, grafana, jaeger
	$(COMPOSE) up -d postgres redis minio prometheus grafana jaeger

dev-down: ## Stop the dev stack (volumes preserved)
	$(COMPOSE) down

docker-build: ## Build the production image
	docker build -f docker/Dockerfile -t strato:latest .

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.txt coverage.html proto/gen

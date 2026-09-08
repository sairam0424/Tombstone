.DEFAULT_GOAL := help
# Use docker context desktop-linux automatically (macOS Docker Desktop)
COMPOSE := docker compose -f infra/docker-compose.yml
# go.work's own `go 1.22.0` directive is lower than every service's real
# go.mod `go 1.25.0` directive -- modern Go refuses to proceed on that
# mismatch ("go.work lists go 1.22.0... update it"). CI never hits this
# because every Go step there sets GOWORK=off explicitly (see ci.yml);
# `make test`/`make build` never did, so running either locally failed
# immediately on the very first Go service, before testing anything.
export GOWORK := off

GO_SERVICES := flag-api gateway evaluator gitops-sync ast-rewriter marketplace tombstone-operator

.PHONY: help dev down migrate seed gen-proto gen-sqlc test build lint

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

dev: ## Start full local dev stack, run migrations and seed
	$(COMPOSE) up --build -d
	@echo "Waiting for postgres..."
	@sleep 8
	$(MAKE) migrate
	$(MAKE) seed
	@echo "Tombstone running: API=http://localhost:8081 Gateway=http://localhost:8080 Dashboard=http://localhost:3000"

down: ## Stop all services
	$(COMPOSE) down

migrate: ## Apply baseline schema and all incremental migrations
	docker exec $$($(COMPOSE) ps -q postgres) psql -U tombstone -d tombstone -f /docker-entrypoint-initdb.d/schema.sql 2>/dev/null || \
	$(COMPOSE) exec -T postgres psql -U tombstone -d tombstone < services/flag-api/internal/db/schema.sql
	@for f in $$(ls services/flag-api/internal/db/migrations/*.sql 2>/dev/null | sort); do \
		echo "Applying migration: $$f"; \
		$(COMPOSE) exec -T postgres psql -U tombstone -d tombstone < $$f; \
	done

seed: ## Insert sample flags into dev database
	bash scripts/seed-dev.sh

gen-proto: ## Generate Go stubs from proto files (requires protoc)
	bash scripts/gen-proto.sh

gen-sqlc: ## Regenerate flag-api's type-safe query package from internal/db/queries/*.sql (requires sqlc, DATA-1b)
	cd services/flag-api && sqlc generate

test: ## Run all tests (Go + TypeScript + Python)
	@echo "--- Go tests ---"
	@for svc in $(GO_SERVICES); do \
		echo "-- $$svc --"; \
		(cd services/$$svc && go test ./...) || exit 1; \
	done
	@echo "--- Python intelligence tests ---"
	cd services/intelligence && uv sync --all-packages && uv run pytest tests/
	@echo "--- TypeScript SDK tests ---"
	cd packages/sdks/@flagmind/core && npm run test
	@echo "--- Dashboard tests ---"
	npm run test --workspace=workspace-dashboard

build: ## Build all Go binaries and TypeScript packages
	@for svc in $(GO_SERVICES); do \
		echo "-- building $$svc --"; \
		(cd services/$$svc && go build -o bin/$$svc ./cmd/main.go) || exit 1; \
	done
	npm run build --workspaces --if-present

lint: ## Lint all code
	@command -v golangci-lint >/dev/null 2>&1 && \
	  (cd services/flag-api && golangci-lint run ./...; cd ../gateway && golangci-lint run ./...) || \
	  echo "golangci-lint not installed, skipping Go lint"
	@command -v ruff >/dev/null 2>&1 && \
	  ruff check services/intelligence/ || echo "ruff not installed, skipping Python lint"
	npm run lint --workspace=workspace-dashboard --if-present

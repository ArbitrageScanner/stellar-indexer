.PHONY: build test itest race lint fmt run-direct up up-direct down logs invariants e2e
GO ?= go
DB ?= postgres://indexer:indexer@localhost:5432/indexer?sslmode=disable

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/indexer ./cmd/indexer
# Unit + fixture tests (no services needed).
test:
	$(GO) test -count=1 ./...
# + Postgres integration and queue pipeline tests.
itest:
	TEST_DATABASE_URL=$(DB) $(GO) test -count=1 ./...
race:
	TEST_DATABASE_URL=$(DB) $(GO) test -race -count=1 ./...
lint:
	golangci-lint run ./...
fmt:
	gofmt -w cmd internal web
# Queue-less pipeline + API in one process against $(DB).
run-direct: build
	DATABASE_URL=$(DB) ./bin/indexer run
up:
	docker compose up -d --build --wait
up-direct:
	docker compose --profile direct up -d --build postgres indexer
down:
	docker compose down
logs:
	docker compose logs -f --tail=100
# Data-integrity checks against a running index.
invariants:
	psql "$(DB)" -f scripts/check-invariants.sql
# Browser smoke test of every UI page: make e2e URL=http://localhost:8080
e2e:
	node web/e2e/smoke.mjs $(or $(URL),http://localhost:8080)

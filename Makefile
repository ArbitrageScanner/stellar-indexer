.PHONY: build test itest run-testnet ui-dev e2e up down
build:
	CGO_ENABLED=0 go build -o bin/indexer ./cmd/indexer
test:
	go test ./...
# Integration tests need Postgres: make itest TEST_DATABASE_URL=postgres://...
itest:
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -count=1 ./...
run-testnet: build
	STELLAR_NETWORK=testnet ./bin/indexer run
# Serve the UI from web/static (edit + reload) against the configured database.
ui-dev: build
	UI_DIR=web/static ./bin/indexer api
# Browser smoke test of every UI page: make e2e URL=http://localhost:8080
e2e:
	node web/e2e/smoke.mjs $(or $(URL),http://localhost:8080)
up:
	docker compose up -d --build
down:
	docker compose down

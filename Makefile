# Zemi Fleet V2 - Makefile (Let's Go Further ch. 20)
#
# Two machines, two target groups:
#   dev/*      your machine: the API (`go run`) and your locally installed PostgreSQL, no Docker.
#              Create the database and role once by hand (README "Development"), then set
#              DATABASE_URL in app.env. Needs the golang-migrate CLI on PATH.
#   staging/*  Docker Desktop: a Linux binary is built here, baked into the image and run
#              with Compose. That image is exactly what production runs.
#
# Production is never driven from a laptop: .github/workflows/deploy.yml builds the
# same binary the same way, but only after the test workflow has passed.

-include app.env
export

# Stop Git Bash on Windows rewriting /paths passed to docker.
export MSYS_NO_PATHCONV=1

# Version stamped into every binary (Let's Go Further ch. 20.6).
VERSION         := $(shell git describe --always --dirty --tags 2>/dev/null || echo dev)
LDFLAGS         := -s -X main.version=$(VERSION)

EXE             := $(shell go env GOEXE)
STAGING_COMPOSE := docker compose -p zemi-staging -f docker-compose.yml
STAGING_URL     := http://localhost:$(or $(STAGING_API_PORT),8080)

# ==================================================================================== #
# HELPERS
# ==================================================================================== #

## help: print this help message
.PHONY: help
help:
	@echo 'Usage: make <target>'
	@awk -F': ' '/^## /{sub(/^## /,""); printf "  %-32s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: confirm
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

# ==================================================================================== #
# DEVELOPMENT  (your machine)
# ==================================================================================== #

## dev/run: run the API natively with go run (http://localhost:$PORT)
.PHONY: dev/run
dev/run:
	go run -ldflags='-X main.version=$(VERSION)' ./cmd/api

# ---- database (local PostgreSQL, Let's Go Further ch. 5-6) ---------------------------

## dev/db/psql: connect to the local development database using psql
.PHONY: dev/db/psql
dev/db/psql:
	psql "$(DATABASE_URL)"

## dev/db/migrations/new name=$1: create a new pair of migration files
.PHONY: dev/db/migrations/new
dev/db/migrations/new:
	@test -n "$(name)" || (echo 'usage: make dev/db/migrations/new name=create_x_table' && exit 1)
	@echo 'Creating migration files for $(name)...'
	migrate create -seq -ext=.sql -dir=./db/migrations $(name)

## dev/db/migrations/up: apply all up migrations
.PHONY: dev/db/migrations/up
dev/db/migrations/up: confirm
	@echo 'Running up migrations...'
	migrate -path ./db/migrations -database "$(DATABASE_URL)" up

## dev/db/migrations/down: roll back the most recent migration
.PHONY: dev/db/migrations/down
dev/db/migrations/down: confirm
	@echo 'Rolling back the most recent migration...'
	migrate -path ./db/migrations -database "$(DATABASE_URL)" down 1

## dev/db/migrations/version: show the current migration version
.PHONY: dev/db/migrations/version
dev/db/migrations/version:
	migrate -path ./db/migrations -database "$(DATABASE_URL)" version

# ---- quality control (ch. 20.3) - the test workflow runs the same checks ------------

## dev/tidy: tidy module dependencies and format all .go files
.PHONY: dev/tidy
dev/tidy:
	go mod tidy
	go fmt ./...

## dev/audit: tidy check, verify, gofmt, vet, staticcheck, tests
.PHONY: dev/audit
dev/audit:
	go mod tidy -diff
	go mod verify
	test -z "$$(gofmt -l .)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
	go test -vet=off -count=1 ./...

## dev/test: run all unit tests
.PHONY: dev/test
dev/test:
	go test -count=1 ./...

## dev/test/race: run tests with the race detector (needs cgo/gcc; CI always runs it)
.PHONY: dev/test/race
dev/test/race:
	CGO_ENABLED=1 go test -race -count=1 ./...

## dev/test/cover: run tests and write an HTML coverage report to coverage.html
.PHONY: dev/test/cover
dev/test/cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# ---- build (ch. 20.5) ----------------------------------------------------------------

## dev/build: build a native binary into ./bin (run it with ./bin/api)
.PHONY: dev/build
dev/build:
	go build -trimpath -ldflags='$(LDFLAGS)' -o=./bin/api$(EXE) ./cmd/api
	@echo 'built ./bin/api$(EXE) version $(VERSION)'

# ==================================================================================== #
# STAGING  (Docker Desktop - the same binary and image production runs)
# ==================================================================================== #

## staging/binary: cross-compile the Linux binary that the image will contain
.PHONY: staging/binary
staging/binary:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o=./bin/linux_amd64/api ./cmd/api
	@echo 'built ./bin/linux_amd64/api version $(VERSION)'

## staging/build: build the binary, then package it into the zemi-fleet-api:staging image
.PHONY: staging/build
staging/build: staging/binary
	API_BUILD_TARGET=prebuilt $(STAGING_COMPOSE) build api

## staging/up: build, then start postgres -> migrate -> api on Docker Desktop
.PHONY: staging/up
staging/up: staging/build
	API_BUILD_TARGET=prebuilt $(STAGING_COMPOSE) up -d --wait
	@echo 'Staging API $(VERSION): $(STAGING_URL)/api/v2/healthz'

## staging/smoke: check that the staging API answers its healthcheck
.PHONY: staging/smoke
staging/smoke:
	curl --fail --silent --show-error $(STAGING_URL)/api/v2/healthz

## staging/logs: follow the API container logs
.PHONY: staging/logs
staging/logs:
	$(STAGING_COMPOSE) logs -f api

## staging/ps: list staging containers and their health
.PHONY: staging/ps
staging/ps:
	$(STAGING_COMPOSE) ps -a

## staging/migrate: re-run pending migrations against staging Postgres
.PHONY: staging/migrate
staging/migrate:
	$(STAGING_COMPOSE) run --rm migrate

## staging/psql: open psql inside the staging Postgres container
.PHONY: staging/psql
staging/psql:
	$(STAGING_COMPOSE) exec postgres psql -U $(or $(DB_USER),zemi) -d $(or $(DB_NAME),zemi_fleet)

## staging/down: stop staging containers (data is kept)
.PHONY: staging/down
staging/down:
	$(STAGING_COMPOSE) down

## staging/reset: stop staging and delete its database volume
.PHONY: staging/reset
staging/reset: confirm
	$(STAGING_COMPOSE) down -v

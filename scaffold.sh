#!/usr/bin/env bash
# Scaffold Zemi Fleet V2 from the project root.
# Run from Git Bash:
#   chmod +x scaffold.sh
#   ./scaffold.sh
#
# Creates directories and placeholder files only. Never overwrites existing
# files and does not generate application code or install dependencies.

set -euo pipefail

ROOT_DIR="$(pwd)"
PROJECT_NAME="zemi-fleet"

if [[ "$(basename "$ROOT_DIR")" != "$PROJECT_NAME" ]]; then
  echo "Warning: current directory is '$(basename "$ROOT_DIR")', not '$PROJECT_NAME'."
  read -r -p "Scaffold into $ROOT_DIR anyway? [y/N] " answer
  [[ "$answer" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 1; }
fi

created_dirs=0
created_files=0
skipped_files=0

make_dir() {
  if [[ ! -d "$1" ]]; then
    mkdir -p "$1"
    created_dirs=$((created_dirs + 1))
  fi
}

# touch_file PATH           -> creates an empty file if missing
touch_file() {
  if [[ -e "$1" ]]; then
    skipped_files=$((skipped_files + 1))
    return 0
  fi
  mkdir -p "$(dirname "$1")"
  : > "$1"
  created_files=$((created_files + 1))
}

# make_file PATH <<'EOF'    -> creates the file with heredoc content if missing
make_file() {
  local file="$1"
  if [[ -e "$file" ]]; then
    cat > /dev/null            # discard the heredoc, keep the existing file
    skipped_files=$((skipped_files + 1))
    return 0
  fi
  mkdir -p "$(dirname "$file")"
  cat > "$file"
  created_files=$((created_files + 1))
}

echo "Scaffolding Zemi Fleet V2 in: $ROOT_DIR"

# ---------------------------------------------------------------------------
# Directories
# ---------------------------------------------------------------------------
for dir in \
  .github/workflows \
  cmd/api \
  internal/config \
  internal/domain \
  internal/geo \
  internal/batching \
  internal/matching \
  internal/repository/sqlc \
  internal/mock \
  internal/service \
  internal/api \
  internal/token \
  internal/util \
  db/migrations \
  db/query \
  db/seed \
  api \
  docs/diagrams \
  postman \
  scripts; do
  make_dir "$dir"
done

# ---------------------------------------------------------------------------
# Empty placeholders
# ---------------------------------------------------------------------------
for file in \
  .github/workflows/test.yml \
  .github/workflows/staging.yml \
  .github/workflows/deploy.yml \
  cmd/api/main.go \
  internal/config/config.go \
  internal/domain/models.go \
  internal/domain/errors.go \
  internal/geo/geo.go \
  internal/geo/geo_test.go \
  internal/batching/batcher.go \
  internal/batching/sequencer.go \
  internal/batching/scoring.go \
  internal/batching/batcher_test.go \
  internal/matching/cone.go \
  internal/matching/preference.go \
  internal/matching/cone_test.go \
  internal/matching/preference_test.go \
  internal/repository/store.go \
  internal/repository/tx_accept_batch.go \
  internal/repository/tx_create_batches.go \
  internal/repository/main_test.go \
  internal/repository/tx_accept_batch_test.go \
  internal/mock/store.go \
  internal/service/batch_service.go \
  internal/service/matching_service.go \
  internal/service/order_service.go \
  internal/service/rider_service.go \
  internal/api/server.go \
  internal/api/routes.go \
  internal/api/middleware.go \
  internal/api/errors.go \
  internal/api/response.go \
  internal/api/validator.go \
  internal/api/dto.go \
  internal/api/batch_handler.go \
  internal/api/matching_handler.go \
  internal/api/order_handler.go \
  internal/api/rider_handler.go \
  internal/api/health_handler.go \
  internal/util/id.go \
  internal/util/random.go \
  db/migrations/000001_init_schema.up.sql \
  db/migrations/000001_init_schema.down.sql \
  db/query/order.sql \
  db/query/rider.sql \
  db/query/preference.sql \
  db/query/batch.sql \
  db/query/area.sql \
  db/query/category.sql \
  db/seed/seed.sql \
  api/openapi.yaml \
  docs/DOCUMENTATION.md \
  postman/Zemi_Fleet_V2.postman_collection.json \
  postman/Zemi_Fleet_V2.postman_environment.json; do
  touch_file "$file"
done
# Note: diagrams (architecture.png, erd.png) are not created as empty files;
# an empty .png is an invalid image. Export real ones into docs/diagrams/.

# ---------------------------------------------------------------------------
# Root configuration with starter content
# ---------------------------------------------------------------------------
make_file ".env.example" <<'EOF'
# Copy to app.env and configure locally. Never commit real secrets.
APP_ENV=development
HTTP_ADDR=:8080
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=change_me
DB_NAME=zemi_fleet
DB_SSLMODE=disable
DATABASE_URL=postgresql://postgres:change_me@localhost:5432/zemi_fleet?sslmode=disable
API_KEYS=replace_with_local_dev_key
RATE_LIMIT_RPS=10
BATCH_MAX_ORDERS=5
BATCH_PICKUP_RADIUS_KM=2
CONE_HALF_ANGLE_DEG=30
CONE_RADIUS_KM=1
RIDER_PAYOUT_BPS=9000
AVG_SPEED_KMH=20
BATCH_TTL=10m
EOF

make_file ".gitignore" <<'EOF'
# Local environment and secrets
app.env
.env
*.env.local

# Go build outputs and coverage
bin/
coverage.out
coverage.html
*.test

# IDE / OS files
.idea/
.vscode/
.DS_Store
Thumbs.db
EOF

# Force LF endings so shell scripts and the Makefile work on Windows checkouts.
make_file ".gitattributes" <<'EOF'
* text=auto eol=lf
*.png binary
EOF

make_file "sqlc.yaml" <<'EOF'
version: "2"
sql:
  - engine: "postgresql"
    schema: "db/migrations"
    queries: "db/query"
    gen:
      go:
        package: "sqlc"
        out: "internal/repository/sqlc"
        sql_package: "pgx/v5"
        emit_interface: true
        emit_json_tags: true
EOF

# Makefile recipe lines MUST start with a real TAB character.
make_file "Makefile" <<'EOF'
include app.env
export

.PHONY: up down migrateup migratedown sqlc mock seed test test-int lint run

up:
	docker compose up --build -d

down:
	docker compose down

migrateup:
	migrate -path db/migrations -database "$(DATABASE_URL)" up

migratedown:
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1

sqlc:
	sqlc generate

mock:
	mockgen -source=internal/repository/store.go -destination=internal/mock/store.go -package=mock

seed:
	psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -f db/seed/seed.sql

test:
	go test -race -cover ./...

test-int:
	go test -race -tags=integration ./...

lint:
	golangci-lint run

run:
	go run ./cmd/api
EOF

make_file "Dockerfile" <<'EOF'
FROM golang:1.23-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H appuser
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY db/migrations /app/db/migrations
USER appuser
EXPOSE 8080
ENTRYPOINT ["/app/api"]
EOF

make_file "docker-compose.yml" <<'EOF'
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: ${DB_NAME:-zemi_fleet}
      POSTGRES_USER: ${DB_USER:-postgres}
      POSTGRES_PASSWORD: ${DB_PASSWORD:-change_me}
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER} -d $${POSTGRES_DB}"]
      interval: 5s
      timeout: 3s
      retries: 10

  api:
    build: .
    env_file:
      - path: app.env
        required: false
    environment:
      DB_HOST: postgres
      DB_PORT: 5432
      DATABASE_URL: postgresql://${DB_USER:-postgres}:${DB_PASSWORD:-change_me}@postgres:5432/${DB_NAME:-zemi_fleet}?sslmode=disable
    ports:
      - "8080:8080"
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  postgres_data:
EOF

make_file ".golangci.yml" <<'EOF'
version: "2"
run:
  timeout: 5m
linters:
  enable:
    - govet
    - staticcheck
    - ineffassign
    - unused
    - errcheck
    - gosec
    - revive
EOF

make_file "README.md" <<'EOF'
# Zemi Fleet V2

REST API for order batching, proximity (60° cone) matching and rider preference matching.

## Stack
- Go 1.23, Gin
- PostgreSQL 16, pgx/v5, sqlc, golang-migrate, Viper
- testify, go.uber.org/mock, faker

## Local setup
1. `cp .env.example app.env` and update local values.
2. Install Go 1.23+, Docker Compose, `migrate` and `sqlc`.
3. `make up`
4. Check `http://localhost:8080/api/v2/healthz`

## Authentication
Protected routes require the `X-API-Key` header. Configure `API_KEYS` in `app.env`.

## Documentation
See [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md).
EOF

make_file "scripts/smoke_test.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-replace_with_local_dev_key}"

curl --fail --silent --show-error "$BASE_URL/api/v2/healthz"
echo
echo "Health endpoint OK. Add authenticated endpoint checks as handlers land."
EOF
chmod +x scripts/smoke_test.sh 2>/dev/null || true

echo
echo "Scaffold complete."
echo "Directories created: $created_dirs"
echo "Files created:       $created_files"
echo "Existing files kept: $skipped_files"
echo
echo "Next steps:"
echo "  cp .env.example app.env"
echo "  go mod init github.com/<you>/zemi-fleet   # only if go.mod does not exist"
echo "  go get github.com/gin-gonic/gin github.com/jackc/pgx/v5 github.com/spf13/viper"
echo "  go mod tidy"
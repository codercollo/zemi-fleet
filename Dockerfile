# Two ways to build the same runtime image:
#
#   --target prebuilt   copies bin/linux_amd64/api, which was built and tested outside Docker.
#                       Used by `make staging/up` and by the deploy workflow, so production
#                       ships the exact binary that passed CI.
#
#   (default) runtime   compiles inside Docker, so anyone can run `docker compose up --build`
#                       without Go or make installed.

# ---- shared runtime base ------------------------------------------------------------
FROM alpine:3.20 AS base
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app
WORKDIR /app
USER app
EXPOSE 4000
ENTRYPOINT ["/app/api"]

# ---- prebuilt: package a binary built by make / CI ----------------------------------
FROM base AS prebuilt
COPY --chown=app:app bin/linux_amd64/api /app/api

# ---- builder: compile inside Docker -------------------------------------------------
FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -X main.version=${VERSION}" -o /out/api ./cmd/api

# ---- runtime (default, last stage) --------------------------------------------------
FROM base AS runtime
COPY --from=builder --chown=app:app /out/api /app/api

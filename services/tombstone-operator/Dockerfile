# Multi-stage build for the Tombstone Kubernetes operator.
# GOWORK=off ensures we use only this module's go.mod (not the workspace).

# -------------------------------------------------------------------
# Stage 1: Build
# -------------------------------------------------------------------
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Download dependencies first (layer caching).
COPY go.mod go.sum ./
RUN GOWORK=off go mod download

# Copy source.
COPY . .

# Build a fully static binary — no CGO, no libc dependency.
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-w -s -X main.version=${VERSION:-dev}" \
    -o /operator \
    ./cmd/main.go

# -------------------------------------------------------------------
# Stage 2: Runtime
# -------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

# Copy the operator binary only — nothing else needed.
COPY --from=builder /operator /operator

# Expose metrics and health probe ports.
EXPOSE 8088 8089

# Run as non-root (distroless nonroot uid = 65532).
USER 65532:65532

ENTRYPOINT ["/operator"]

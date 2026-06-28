FROM golang:1.25-alpine AS builder

WORKDIR /app

ENV GOWORK=off

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /marketplace ./cmd/main.go

# ── runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19
LABEL org.opencontainers.image.source="https://github.com/sairam0424/Tombstone" \
      org.opencontainers.image.description="Tombstone marketplace service" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

ENV GOWORK=off

COPY --from=builder /marketplace /app/marketplace

EXPOSE 8086

ENTRYPOINT ["/app/marketplace"]

FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN GOWORK=off go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOWORK=off CGO_ENABLED=0 GOOS=linux go build -o /gateway ./cmd/main.go

FROM alpine:3.19
LABEL org.opencontainers.image.source="https://github.com/sairam0424/Tombstone" \
      org.opencontainers.image.description="Tombstone gateway service" \
      org.opencontainers.image.licenses="MIT"
RUN apk --no-cache add ca-certificates
COPY --from=builder /gateway /gateway
EXPOSE 8080
CMD ["/gateway"]

FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN GOWORK=off go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOWORK=off CGO_ENABLED=0 GOOS=linux go build -o /flag-api ./cmd/main.go

FROM alpine:3.19
RUN apk --no-cache add ca-certificates postgresql-client
COPY --from=builder /flag-api /flag-api
COPY --from=builder /app/internal/db/schema.sql /schema.sql
EXPOSE 8081
CMD ["/flag-api"]

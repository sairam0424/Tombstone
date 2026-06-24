FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN GOWORK=off go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOWORK=off CGO_ENABLED=0 GOOS=linux go build -o /evaluator ./cmd/main.go

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /evaluator /evaluator
EXPOSE 8082
CMD ["/evaluator"]

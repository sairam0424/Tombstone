# ---- build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Disable go workspaces so the build is fully self-contained.
ENV GOWORK=off

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -o /ast-rewriter ./cmd/main.go

# ---- runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /ast-rewriter /app/ast-rewriter

EXPOSE 8085

ENTRYPOINT ["/app/ast-rewriter"]

FROM golang:1.22-alpine AS builder

WORKDIR /app

ENV GOWORK=off

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /marketplace ./cmd/main.go

# ── runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

ENV GOWORK=off

COPY --from=builder /marketplace /app/marketplace

EXPOSE 8086

ENTRYPOINT ["/app/marketplace"]

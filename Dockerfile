# Stage 1: Build
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /bin/retrievr-mcp \
    ./cmd/retrievr-mcp

# Stage 2: Runtime
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 1000 -S rtv && \
    adduser -u 1000 -S rtv -G rtv

WORKDIR /app

COPY --from=builder /bin/retrievr-mcp /app/retrievr-mcp
COPY configs/retrievr-mcp.yaml /app/configs/retrievr-mcp.yaml
COPY versions.yaml /app/versions.yaml

USER rtv:rtv

EXPOSE 8099

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:8099/health || exit 1

ENTRYPOINT ["./retrievr-mcp"]
CMD ["--config", "configs/retrievr-mcp.yaml"]

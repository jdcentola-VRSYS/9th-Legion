# syntax=docker/dockerfile:1

# ===== Builder stage =====
# Use a modern Go image; go.mod requires go >= 1.24.5
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Dependencies needed for go modules + TLS
RUN apk add --no-cache git ca-certificates && update-ca-certificates

# Allow Go to auto-download the matching toolchain if needed
ENV GOTOOLCHAIN=auto

# Go module files first (better caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source
COPY . .

# Build the Commander binary
# (root package is Commander — main.go in repo root)
RUN CGO_ENABLED=0 GOOS=linux go build -o legion-commander .

# ===== Runtime stage =====
FROM alpine:3.20

WORKDIR /app

# CA certs so HTTPS calls work if we ever need them
RUN apk add --no-cache ca-certificates && update-ca-certificates

# Copy the compiled binary from the builder
COPY --from=builder /app/legion-commander /usr/local/bin/legion-commander

# Environment-driven config (from Task 34)
ENV LISTEN_ADDR=":8080" \
    PROVIDER_MODE="off" \
    LEGION_TOKEN="change-me"

EXPOSE 8080

# Healthcheck hits Commander /healthz
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1

CMD ["legion-commander"]

# =============================================================================
# iCloud Backup Server — Multi-stage Docker build
# =============================================================================
# Pure Go binary — no CGO dependencies. BadgerDB, tusd, and chi are all
# pure Go, so CGO can remain disabled for smaller, statically-linked builds.
#
# Usage:
#   docker build -t files-nest-server .
#   docker run -p 8080:8080 \
#     -e BACKUP_USER=admin \
#     -e BACKUP_PASS=secret \
#     -e STORAGE_PATH=/data \
#     -v /path/to/data:/data \
#     files-nest-server
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Build the Go binary
# ---------------------------------------------------------------------------
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src

# Cache dependency downloads in a separate layer.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a statically-linked binary.
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -extldflags=-static" -o /bin/server .

# ---------------------------------------------------------------------------
# Stage 2: Minimal runtime image
# ---------------------------------------------------------------------------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# Create a non-root user for the server process.
RUN adduser -D -h /home/server server

# Copy the server binary from the builder stage.
COPY --from=builder /bin/server /usr/local/bin/server

# Create the default storage path with correct ownership.
RUN mkdir -p /data && chown -R server:server /data

# Switch to the non-root user.
USER server
WORKDIR /home/server

# Expose the HTTP port the server listens on.
EXPOSE 8080

# Default environment variables — override via docker run -e or docker-compose.
ENV STORAGE_PATH=/data
ENV PORT=8080

# Health check: the /health endpoint returns 200 when the server is ready.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/server"]

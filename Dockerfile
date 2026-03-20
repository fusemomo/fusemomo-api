# Stage 1: builder
# Compile a fully static binary with all CGO disabled.
FROM golang:1.25-alpine AS builder

# Install git (needed by go mod download for some VCS deps)
# and ca-certificates (baked into the final image via this stage)
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy dependency manifests first — Docker layer cache will skip
# the expensive `go mod download` when only source files change.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy the rest of the source tree
COPY . .

# Build a statically-linked Linux binary.
# Flags:
#   -trimpath            — strip local build paths from stack traces
#   -ldflags "-s -w"     — strip debug symbols → ~30% smaller binary
#   CGO_ENABLED=0        — no libc dependency → truly scratch-compatible
#   GOOS/GOARCH          — explicit target (safety net for cross-compilation)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /app/dist/server \
      ./cmd/api/main.go

# Stage 2: production image
# Uses distroless — no shell, no package manager, minimal attack surface.
# Alternatives: scratch (even smaller), alpine (has a shell for debugging).
FROM gcr.io/distroless/static-debian12:nonroot AS production

# Bake in CA certs and timezone data from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo                 /usr/share/zoneinfo

# Copy only the compiled binary — nothing else
COPY --from=builder /app/dist/server /server

# Digital Ocean App Platform / Kubernetes convention:
# The platform sets PORT at runtime, defaulting to 8080 by the platform
# and 8000 by config.go if PORT is unset.
EXPOSE 8000

# Run as the prebuilt non-root user provided by distroless (uid 65532)
USER nonroot

# SIGTERM is caught by gracefulShutdown() in main.go (5-second drain window)
ENTRYPOINT ["/server"]

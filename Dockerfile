# Multi-stage build: compile in a big image that has Go, ship a tiny image
# that contains only the compiled binary.

# ---- Stage 1: build ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Copy only the dependency files first and download modules. Docker caches
# this layer, so dependencies are re-downloaded only when go.mod/go.sum change,
# not on every code change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0  -> pure Go, statically linked binary (no C libraries needed at runtime)
# -trimpath      -> no paths from the build machine inside the binary
# -ldflags=-s -w -> strip debug symbols, smaller binary
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---- Stage 2: run ----
# distroless/static: no shell, no package manager, just CA certificates
# (needed for HTTPS calls to the provider), timezone data and a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api

# Documentation only: the port the API listens on (HTTP_PORT default).
EXPOSE 8080

# Never run as root inside the container.
USER nonroot:nonroot

ENTRYPOINT ["/api"]

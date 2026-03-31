# Build context must be the repository root so both modules are available.
# Usage: docker build -f key-manager/Dockerfile -t key-manager:dev .

FROM golang:1.25rc1 AS builder

# Allow the Go toolchain to auto-download the exact version declared in go.mod.
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

# Copy module files for both modules so dependency download is cached.
COPY operator/go.mod operator/go.sum operator/
COPY key-manager/go.mod key-manager/go.sum key-manager/

# Copy the operator API package that the key-manager depends on via replace.
COPY operator/api/ operator/api/

# Download dependencies before copying source to maximise layer cache hits.
WORKDIR /workspace/key-manager
RUN go mod download

# Copy key-manager source.
COPY key-manager/ /workspace/key-manager/

# Build the binary.
RUN CGO_ENABLED=0 GOOS=linux go build -o key-manager ./cmd/main.go

# ---------------------------------------------------------------------------
# Runtime image
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/key-manager/key-manager .

USER 65532:65532

ENTRYPOINT ["/key-manager"]

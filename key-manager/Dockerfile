# Build context must be the repository root so the modules it uses are available.
# Usage: docker build -f key-manager/Dockerfile -t key-manager:dev .

FROM golang:1.24 AS builder

# Allow the Go toolchain to auto-download the exact version declared in go.mod.
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

# Copy module files for both modules so dependency download is cached.
COPY operator/go.mod operator/go.sum operator/
COPY key-manager/go.mod key-manager/go.sum key-manager/

# Copy the operator API package and the shared module that the key-manager
# depends on via replace.
COPY operator/api/ operator/api/
COPY shared/ shared/

# Download dependencies before copying source to maximise layer cache hits.
WORKDIR /workspace/key-manager
RUN go mod download

# Copy key-manager source.
COPY key-manager/ /workspace/key-manager/

# Build the binary.
RUN CGO_ENABLED=0 GOOS=linux go build -o key-manager ./cmd

# ---------------------------------------------------------------------------
# Runtime image
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/key-manager/key-manager .

USER 65532:65532

ENTRYPOINT ["/key-manager"]

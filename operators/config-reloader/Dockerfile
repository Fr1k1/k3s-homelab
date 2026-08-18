# Build stage. golang:1.26-alpine has the full Go toolchain; it never
# ships in the final image, so its size doesn't matter.
FROM golang:1.26-alpine AS builder
WORKDIR /workspace

# Copying go.mod/go.sum and running `go mod download` before copying any
# source code is a deliberate Docker layer-caching trick: as long as
# dependencies don't change, this layer is reused on every rebuild even
# when source files change, so `docker build` doesn't redownload the whole
# module graph on every code edit.
COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY controllers/ controllers/
COPY main.go logging.go ./

# CGO_ENABLED=0 produces a fully static binary with no libc dependency —
# required for the distroless "static" base image below, which has no libc
# at all. GOOS=linux is explicit because this image may be built on a
# non-Linux dev machine (Windows, here) that would otherwise cross-compile
# the wrong target by accident.
RUN CGO_ENABLED=0 GOOS=linux go build -a -o manager main.go logging.go

# Runtime stage - distroless static: no shell, no package manager, nothing
# beyond the one binary and CA certs. Matches the "small, understood,
# nothing hand-waved" bar this repo already holds tools/deploy-verify to
# (see docs/CICD.md) — and a smaller attack surface than any general-purpose
# base image: there's no shell here for an attacker to get if the binary
# were ever compromised.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
# distroless's "nonroot" tag already defaults to this UID, but pinning it
# explicitly documents the intent and survives a base-image variant change.
USER 65532:65532

ENTRYPOINT ["/manager"]

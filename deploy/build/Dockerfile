# deploy/build/Dockerfile
#
# Single shared build for every demo/control-plane image. All Go binaries are
# compiled once in `builder`; each runtime image is a separate final stage
# below that copies only the binaries it needs. Select an image with
# `docker build --target <stage-name>` (or Compose's `build.target`).

FROM golang:1.26 AS builder
WORKDIR /build
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make build
# clientmanager is rebuilt statically (CGO_ENABLED=0) via a direct `go build`,
# NOT `make clientmanager`: the Makefile sets CGO_ENABLED with `:=`, which
# overrides an environment variable of the same name when invoked through
# `make` — `CGO_ENABLED=0 make clientmanager` would silently produce a
# dynamically-linked binary. Its runtime image (smallstep/step-ca, see the
# `ca` stage below) has no libgcc-s1 installed, so a dynamically-linked
# clientmanager would fail to start there. This mirrors the original
# demo/ca/Dockerfile's own build line exactly, including the lack of an
# LDFLAGS version stamp (`-trimpath` only) that every other binary gets via
# `make`.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /build/bin/clientmanager ./cmd/clientmanager

FROM timberio/vector:0.46.0-debian AS vector-source

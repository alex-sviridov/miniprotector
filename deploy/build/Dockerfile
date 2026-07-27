# syntax=docker/dockerfile:1
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
# `ca-demo` stage below) has no libgcc-s1 installed, so a dynamically-linked
# clientmanager would fail to start there. This mirrors the original
# demo/ca/Dockerfile's own build line exactly, including the lack of an
# LDFLAGS version stamp (`-trimpath` only) that every other binary gets via
# `make`. Note `make build` above already compiles clientmanager once (it's
# included in the Makefile's `$(wildcard src/cmd/*)` binary list) before this
# second RUN discards and replaces it with the statically-linked version — a
# few seconds of wasted compute on a cache miss, accepted as a tradeoff rather
# than an oversight, since avoiding it would require either a Makefile change
# or reintroducing a fragile explicit per-image binary list, which is exactly
# what this consolidated Dockerfile was written to eliminate.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /build/bin/clientmanager ./cmd/clientmanager

FROM timberio/vector:0.46.0-debian AS vector-source

FROM debian:bookworm-slim AS backup-host
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalogsync /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY --chmod=0755 demo/backup-host/entrypoint.sh ./entrypoint.sh
ENTRYPOINT ["./entrypoint.sh"]

FROM smallstep/step-ca AS ca-demo

USER root
COPY --chmod=0755 --from=builder /build/bin/clientmanager /usr/local/bin/clientmanager
RUN mkdir -p /home/step/templates /data/client-manager && chown step:step /data/client-manager
COPY --chmod=0644 deploy/control-plane/ca/templates/leaf.tpl /home/step/templates/leaf.tpl
COPY --chmod=0755 demo/ca/entrypoint.sh /home/step/entrypoint.sh
USER step

ENTRYPOINT ["/home/step/entrypoint.sh"]

# uid/gid 1000 below must match the smallstep/step-ca base image's `step` user
# (see the `ca-demo` stage above), so the shared client-manager-data volume stays
# writable by both clientmanager (running as `step` in ca's container) and
# issuer here. If that base image's step uid ever changes, this coupling
# breaks silently with an ownership mismatch.
FROM debian:bookworm-slim AS issuer-demo
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 issuer && useradd -u 1000 -g 1000 -M -s /usr/sbin/nologin issuer \
    && mkdir -p /data && chown issuer:issuer /data

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
USER issuer
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://ca:9000", \
            "--root", "/ca-data/certs/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/ca-data/secrets/password"]

FROM debian:bookworm-slim AS issuer-controlplane
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://step-ca:9000", \
            "--root", "/data/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/data/secrets/password"]

FROM debian:bookworm-slim AS api-server
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/api-server /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/api-server/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]

FROM debian:bookworm-slim AS catalog
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates sqlite3 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/catalog/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]

FROM debian:bookworm-slim AS policy-server
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/policy-server /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/policy-server/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]

FROM debian:bookworm-slim AS log-gateway
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/log-gateway /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/log-gateway/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]

FROM debian:bookworm-slim AS clientmanager-api
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/clientmanager-api /build/bin/clientmanager-admin-api /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/clientmanager-api/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
